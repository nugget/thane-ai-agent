package email

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const (
	// pollNamespace is the opstate namespace for email polling state.
	pollNamespace = "email_poll"

	// DefaultHandlerLoopName is the name of the built-in event-driven
	// loop that receives new-mail wake events when an operator hasn't
	// pointed the poller at a custom handler. The loop definition
	// runtime registers it as a durable built-in whenever email is
	// configured.
	DefaultHandlerLoopName = "email-default-handler"
)

// Poller checks configured email accounts for new messages by comparing
// IMAP UIDs against a persisted high-water mark. It is not a tool — it
// runs as infrastructure code called by the scheduler task executor.
type Poller struct {
	service      *Service
	manager      *Manager
	state        *opstate.Store
	logger       *slog.Logger
	bus          *messages.Bus
	contacts     ContactResolver
	interactions InteractionRecorder
	wakeLoop     messages.LoopWakeTarget
	wakeReady    bool
}

// PollerOption customizes poller behavior.
type PollerOption func(*Poller)

// WithMessageBus enables event-source wake delivery for new-mail
// detection. The poller dispatches a [messages.NewEventSourceEnvelope]
// per account-poll cycle when the bus is configured; without it,
// CheckNewMessages still advances the high-water mark but logs every
// dispatch as suppressed.
func WithMessageBus(bus *messages.Bus) PollerOption {
	return func(p *Poller) { p.bus = bus }
}

// WithContactResolver lets the poller resolve each sender against the
// contact directory, stamping the match (contact_id, contact_name,
// is_owner, contact_status) and its trust_zone on every wake event.
// Without a resolver every sender reads as unmatched — wakes still
// fire, the model just won't see who is writing.
func WithContactResolver(c ContactResolver) PollerOption {
	return func(p *Poller) { p.contacts = c }
}

// WithInteractionRecorder lets the poller record an inbound
// interaction on each matched contact after its wake is delivered.
func WithInteractionRecorder(r InteractionRecorder) PollerOption {
	return func(p *Poller) { p.interactions = r }
}

// WithDefaultWakeLoop overrides the wake target attached to email
// envelopes. Defaults to [DefaultHandlerLoopName] when this option
// isn't passed. Operators can point email wakes at a bespoke handler
// (e.g. an "inbox triage" event-driven loop they declared in YAML)
// by passing one here.
func WithDefaultWakeLoop(target messages.LoopWakeTarget) PollerOption {
	return func(p *Poller) {
		p.wakeLoop = target
		p.wakeReady = true
	}
}

// NewPoller creates an email poller that checks all accounts managed by
// the given Manager and tracks state in the provided opstate store.
// [Service] constructs one through newPoller; this constructor exists
// for callers that hold a Manager without a Service.
func NewPoller(manager *Manager, state *opstate.Store, logger *slog.Logger, opts ...PollerOption) *Poller {
	return newPollerWith(nil, manager, state, logger, opts...)
}

// newPoller creates the poller a Service owns. The service reference
// lets each poll refresh the folder cache the context block renders.
func newPoller(service *Service, state *opstate.Store, logger *slog.Logger, opts ...PollerOption) *Poller {
	return newPollerWith(service, service.manager, state, logger, opts...)
}

func newPollerWith(service *Service, manager *Manager, state *opstate.Store, logger *slog.Logger, opts ...PollerOption) *Poller {
	if logger == nil {
		logger = slog.Default()
	}
	p := &Poller{
		service: service,
		manager: manager,
		state:   state,
		logger:  logger,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if !p.wakeReady {
		p.wakeLoop = messages.LoopWakeTarget{Name: DefaultHandlerLoopName}
		p.wakeReady = true
	}
	return p
}

// highWaterMark is the persisted per-folder cursor: the highest UID
// already dispatched, qualified by the UIDVALIDITY it was observed
// under. A UIDVALIDITY change means the server renumbered the mailbox
// and the stored UID says nothing about it, so the mark reseeds.
type highWaterMark struct {
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
}

// parseHighWaterMark decodes a stored mark. Marks written before
// UIDVALIDITY was tracked are bare decimal UIDs; they decode with a
// zero validity, which the poller adopts on the next successful check
// rather than treating as a mismatch.
func parseHighWaterMark(raw string) (highWaterMark, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return highWaterMark{}, fmt.Errorf("empty high-water mark")
	}
	if raw[0] == '{' {
		var mark highWaterMark
		if err := json.Unmarshal([]byte(raw), &mark); err != nil {
			return highWaterMark{}, fmt.Errorf("decode high-water mark %q: %w", raw, err)
		}
		return mark, nil
	}
	uid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return highWaterMark{}, fmt.Errorf("decode legacy high-water mark %q: %w", raw, err)
	}
	return highWaterMark{UID: uint32(uid)}, nil
}

func (m highWaterMark) encode() string {
	b, _ := json.Marshal(m)
	return string(b)
}

// CheckNewMessages polls every configured account for messages newer
// than its stored high-water mark and dispatches each new message as
// a [messages.LoopEventPayload] to the configured wake_loop target
// (default: [DefaultHandlerLoopName]). Each event's metadata names the
// account, folder, UID, and Message-ID of the message and the trust
// zone of its sender, so the receiving iteration never has to guess
// which mailbox a number belongs to or who is writing.
//
// On first run (no stored high-water mark), the folder's current
// UIDNEXT is recorded silently without reporting anything as new —
// this prevents flooding the agent with the entire inbox on initial
// deployment. The same happens when the folder's UIDVALIDITY differs
// from the stored one, because the stored UID no longer identifies
// anything.
//
// Per-account dispatch batches at [messages.MaxLoopEventsPerWake] and
// the high-water mark advances per successful batch, so a bus failure
// mid-stream preserves prior progress without losing later messages.
//
// Network errors are logged and skipped per-account; a failure on one
// account does not prevent checking others. Returns the total number
// of event-wake notifications delivered (one per inbound message that
// reached the bus), matching forge/media-poller accounting.
func (p *Poller) CheckNewMessages(ctx context.Context) (int, error) {
	accounts := p.manager.AccountNames()
	p.logger.Debug("email poll starting", "accounts", len(accounts))

	var failed int
	var totalNew int
	var delivered int

	for _, name := range accounts {
		p.logger.Debug("email poll checking account", "account", name)

		count, sent, err := p.checkAccount(ctx, name)
		if err != nil {
			failed++
			p.logger.Warn("email poll failed for account",
				"account", name,
				"error", err,
			)
			continue
		}
		totalNew += count
		delivered += sent
	}

	p.logger.Debug("email poll complete",
		"accounts", len(accounts),
		"new_messages", totalNew,
		"delivered_events", delivered,
		"failed", failed,
	)

	if summary := loop.IterationSummary(ctx); summary != nil {
		summary["accounts_checked"] = len(accounts)
		summary["new_messages"] = totalNew
		if delivered > 0 {
			summary["event_wakes"] = delivered
		}
		if failed > 0 {
			summary["failed"] = failed
		}
	}

	return delivered, nil
}

// checkAccount checks a single account's INBOX for new messages and
// dispatches them in [messages.MaxLoopEventsPerWake]-sized batches.
// Returns (newMessageCount, eventsDelivered, err). eventsDelivered is
// the total LoopEventPayloads sent across all successful batches;
// returns 0 (without error) when the message bus is not configured.
// On dispatch failure the high-water mark reflects whatever batches
// did succeed, so the next poll picks up from the last delivered UID
// instead of replaying or losing the whole window.
func (p *Poller) checkAccount(ctx context.Context, accountName string) (int, int, error) {
	client, err := p.manager.Account(accountName)
	if err != nil {
		return 0, 0, fmt.Errorf("get account %q: %w", accountName, err)
	}

	stateKey := accountName + ":" + DefaultFolder

	if p.service != nil {
		p.service.refreshFoldersIfStale(ctx, ResolvedAccount{Name: accountName, Client: client})
	}

	status, err := client.MailboxStatus(ctx, DefaultFolder)
	if err != nil {
		return 0, 0, fmt.Errorf("status %q: %w", accountName, err)
	}
	// UIDNEXT is the UID the next message will get, so everything that
	// exists now has a UID strictly below it.
	current := highWaterMark{UIDValidity: status.UIDValidity}
	if status.UIDNext > 0 {
		current.UID = status.UIDNext - 1
	}

	storedStr, err := p.state.Get(pollNamespace, stateKey)
	if err != nil {
		return 0, 0, fmt.Errorf("get high-water mark %q: %w", stateKey, err)
	}

	if storedStr == "" {
		p.logger.Info("email poll first run, seeding high-water mark",
			"account", accountName,
			"uid", current.UID,
			"uid_validity", current.UIDValidity,
		)
		return 0, 0, p.setHighWaterMark(stateKey, current)
	}
	stored, err := parseHighWaterMark(storedStr)
	if err != nil {
		p.logger.Warn("corrupt high-water mark, reseeding",
			"account", accountName,
			"stored", storedStr,
			"error", err,
		)
		return 0, 0, p.setHighWaterMark(stateKey, current)
	}

	if stored.UIDValidity != 0 && stored.UIDValidity != current.UIDValidity {
		// The server renumbered the mailbox. Nothing about the old UID
		// carries over, and replaying from zero would flood the
		// handler with the whole folder, so reseed at the current top.
		p.logger.Info("email poll mailbox UIDVALIDITY changed, reseeding high-water mark",
			"account", accountName,
			"stored_uid_validity", stored.UIDValidity,
			"uid_validity", current.UIDValidity,
			"uid", current.UID,
		)
		return 0, 0, p.setHighWaterMark(stateKey, current)
	}
	if stored.UIDValidity == 0 {
		// Legacy bare-UID mark: adopt the validity we can now see.
		stored.UIDValidity = current.UIDValidity
	}

	p.logger.Debug("email poll querying IMAP",
		"account", accountName,
		"since_uid", stored.UID,
	)

	listed, err := client.ListMessages(ctx, ListOptions{
		Folder:   DefaultFolder,
		SinceUID: stored.UID,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list messages %q: %w", accountName, err)
	}
	if listed.UIDValidity != 0 && listed.UIDValidity != stored.UIDValidity {
		// Changed between STATUS and SELECT; next poll reseeds.
		p.logger.Info("email poll UIDVALIDITY changed mid-poll, deferring",
			"account", accountName,
			"uid_validity", listed.UIDValidity,
		)
		return 0, 0, nil
	}
	newMessages := listed.Envelopes

	p.logger.Debug("email poll IMAP results",
		"account", accountName,
		"new_messages", len(newMessages),
	)

	if len(newMessages) == 0 {
		return 0, 0, nil
	}

	// Record the highest UID across ALL fetched messages (pre-filter)
	// so a successful run can advance the high-water mark past any
	// self-sent UIDs above the last delivered batch. The advance is
	// NOT applied here — that would lose mail on dispatch failure.
	overallMax := stored
	for _, env := range newMessages {
		if env.UID > overallMax.UID {
			overallMax.UID = env.UID
		}
	}

	preFilterCount := len(newMessages)
	newMessages = p.filterSelfSent(accountName, newMessages)
	if preFilterCount != len(newMessages) {
		p.logger.Debug("email poll filtered self-sent messages",
			"account", accountName,
			"before", preFilterCount,
			"after", len(newMessages),
		)
	}

	delivered, err := p.dispatchAccountBatches(ctx, accountName, stateKey, stored, newMessages)
	if err != nil {
		// Partial progress is already persisted by dispatchAccountBatches
		// (per-batch high-water advance on success). The next poll picks
		// up from the last-successful UID.
		return preFilterCount, delivered, err
	}

	// All filtered messages delivered. Bump the high-water mark past
	// any self-sent UIDs above the final batch so the next poll
	// doesn't re-observe them. No-op when the dispatched batches
	// already covered overallMax (the common case).
	if overallMax.UID > stored.UID {
		if err := p.setHighWaterMark(stateKey, overallMax); err != nil {
			return preFilterCount, delivered, err
		}
	}
	return preFilterCount, delivered, nil
}

// dispatchAccountBatches splits the account's filtered new-message
// list into [messages.MaxLoopEventsPerWake]-sized batches, dispatches
// each as a single event-source envelope, and advances the high-water
// mark after each successful batch. Per-batch advancement is the
// retry-safety lever: a bus failure mid-stream loses the failing
// batch's progress but preserves all prior batches; the next poll
// picks up from the last successful UID instead of replaying every
// previously-delivered message or losing the whole window.
//
// Batches are ordered oldest-first so partial progress always advances
// monotonically. The wake target's own tags pass through untouched; the
// poller adds none, because a tag is a per-batch union that cannot name
// any one message — identity rides in each event's metadata instead.
//
// Returns the total number of events delivered across all successful
// batches. A nil message bus is a no-op (logs and returns 0) so a
// transient bus-missing window doesn't error.
func (p *Poller) dispatchAccountBatches(ctx context.Context, accountName, stateKey string, currentMark highWaterMark, newMessages []Envelope) (int, error) {
	if p.bus == nil {
		p.logger.Warn("email message bus not configured; new mail observed but not dispatched",
			"account", accountName,
			"new_messages", len(newMessages),
		)
		return 0, nil
	}
	if len(newMessages) == 0 {
		return 0, nil
	}

	// IMAP returns newest-first; flip to oldest-first so per-batch
	// progress always advances the high-water mark monotonically.
	ordered := make([]Envelope, len(newMessages))
	for i, env := range newMessages {
		ordered[len(newMessages)-1-i] = env
	}

	const batchSize = messages.MaxLoopEventsPerWake
	delivered := 0
	currentHigh := currentMark
	for start := 0; start < len(ordered); start += batchSize {
		end := min(start+batchSize, len(ordered))
		chunk := ordered[start:end]
		lookup := newIdentityLookup(ctx, p.contacts, p.logger)
		events, batchMaxUID := p.buildBatchEvents(accountName, chunk, lookup)

		target := p.wakeLoop
		env, err := messages.NewEventSourceEnvelope(
			messages.Identity{Kind: messages.IdentitySystem, Name: "email_poller"},
			target,
			"email_poll",
			events,
		)
		if err != nil {
			return delivered, fmt.Errorf("build email wake envelope (batch %d-%d of %d): %w", start, end, len(ordered), err)
		}
		if _, err := p.bus.Send(ctx, env); err != nil {
			return delivered, fmt.Errorf("deliver email wake envelope (batch %d-%d of %d): %w", start, end, len(ordered), err)
		}
		delivered += len(events)
		p.recordInboundInteractions(ctx, accountName, chunk, lookup)

		// Persist per-batch progress only when this batch's max UID
		// actually exceeds the running mark — guards against
		// non-monotonic advancement if the slice ever arrives
		// reordered.
		if batchMaxUID > currentHigh.UID {
			currentHigh.UID = batchMaxUID
			if err := p.setHighWaterMark(stateKey, currentHigh); err != nil {
				return delivered, err
			}
		}
	}
	return delivered, nil
}

// buildBatchEvents converts a chunk of envelopes into structured
// LoopEventPayloads and reports the highest UID observed in the chunk.
// Every fact the handler needs to act on a message travels in the
// event's metadata: the account and folder the UID belongs to, the
// Message-ID, the sender's address and display name, and the contact
// directory's answer about the sender — contact_id, contact_name,
// is_owner, contact_status, and the effective trust_zone ("unknown"
// for a stranger). is_owner says the record is the operator's, not that
// the operator wrote the message; a From header is a claim.
func (p *Poller) buildBatchEvents(accountName string, chunk []Envelope, lookup *identityLookup) ([]messages.LoopEventPayload, uint32) {
	events := make([]messages.LoopEventPayload, 0, len(chunk))
	var maxUID uint32
	for _, env := range chunk {
		match := lookup.resolve(env.From)
		if env.UID > maxUID {
			maxUID = env.UID
		}
		metadata := map[string]string{
			"account":        accountName,
			"folder":         DefaultFolder,
			"uid":            strconv.FormatUint(uint64(env.UID), 10),
			"from":           env.From.String(),
			"from_address":   env.From.Key(),
			"trust_zone":     match.TrustZone,
			"contact_status": string(match.Status),
			"is_owner":       "false",
		}
		if env.From.Name != "" {
			metadata["from_name"] = env.From.Name
		}
		if env.MessageID != "" {
			metadata["message_id"] = env.MessageID
		}
		if match.Binding != nil {
			metadata["contact_id"] = match.Binding.ContactID
			metadata["contact_name"] = match.Binding.ContactName
			metadata["is_owner"] = strconv.FormatBool(match.Binding.IsOwner)
		}
		events = append(events, messages.LoopEventPayload{
			Source:     "email_poll",
			Type:       "new_message",
			ID:         fmt.Sprintf("%s:%d", accountName, env.UID),
			Title:      env.Subject,
			Summary:    fmt.Sprintf("From %s (account %s, folder %s)", env.From.String(), accountName, DefaultFolder),
			ObservedAt: env.Date,
			Metadata:   metadata,
		})
	}
	return events, maxUID
}

// recordInboundInteractions notes, once per matched contact in a
// delivered batch, that mail arrived from them: the newest message's
// date wins, and the recorder ignores anything older than what it
// already holds. Failures are logged and do not fail the poll — the
// wake has already been delivered.
func (p *Poller) recordInboundInteractions(ctx context.Context, accountName string, chunk []Envelope, lookup *identityLookup) {
	if p.interactions == nil {
		return
	}
	latest := make(map[string]Interaction)
	for _, env := range chunk {
		match := lookup.resolve(env.From)
		if match.Binding == nil || match.Binding.ContactID == "" {
			continue
		}
		at := env.Date
		if at.IsZero() {
			at = time.Now()
		}
		in, seen := latest[match.Binding.ContactID]
		if !seen || at.After(in.At) {
			latest[match.Binding.ContactID] = Interaction{
				ContactID: match.Binding.ContactID,
				At:        at,
				Direction: DirectionInbound,
				Account:   accountName,
				MessageID: env.MessageID,
			}
		}
	}
	for _, in := range latest {
		if err := p.interactions.RecordEmailInteraction(ctx, in); err != nil {
			p.logger.Warn("recording inbound email interaction failed",
				"account", accountName,
				"contact_id", in.ContactID,
				"error", err,
			)
		}
	}
}

// setHighWaterMark persists the per-account cursor without consulting
// prior state. The caller is responsible for monotonicity;
// dispatchAccountBatches enforces that by tracking the running mark
// across batches.
func (p *Poller) setHighWaterMark(stateKey string, mark highWaterMark) error {
	if err := p.state.Set(pollNamespace, stateKey, mark.encode()); err != nil {
		return fmt.Errorf("update high-water mark %q: %w", stateKey, err)
	}
	return nil
}

// filterSelfSent removes messages whose From is the account's own
// default_from address. This prevents the agent from triaging its own
// outbound replies that appear in INBOX (Bcc-to-self, server-side
// copies). The suppression is logged at Info with the UID so a forged
// From that silenced a wake is visible afterwards.
func (p *Poller) filterSelfSent(accountName string, envelopes []Envelope) []Envelope {
	acctCfg, err := p.manager.AccountConfig(accountName)
	if err != nil || acctCfg.DefaultFrom == "" {
		return envelopes // can't filter without a configured From address
	}
	own, err := parseAddress(acctCfg.DefaultFrom)
	if err != nil {
		return envelopes
	}

	ownKey := own.Key()
	filtered := make([]Envelope, 0, len(envelopes))
	for _, env := range envelopes {
		if env.From.Key() == ownKey {
			p.logger.Info("email poll skipping self-sent message",
				"account", accountName,
				"uid", env.UID,
				"message_id", env.MessageID,
				"subject", env.Subject,
			)
			continue
		}
		filtered = append(filtered, env)
	}
	return filtered
}
