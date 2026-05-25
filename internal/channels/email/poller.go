package email

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

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
	manager   *Manager
	state     *opstate.Store
	logger    *slog.Logger
	bus       *messages.Bus
	contacts  ContactResolver
	wakeLoop  messages.LoopWakeTarget
	wakeReady bool
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

// WithContactResolver lets the poller translate each sender into a
// trust zone for wake-tag classification. Contacts the resolver
// recognises stamp tags like "owner" / "trusted" / "household" /
// "known" on the wake envelope; unrecognised senders stamp
// "stranger". Without a resolver the poller falls back to "stranger"
// for every message — wakes still fire, the model just won't see the
// trust-derived hint.
func WithContactResolver(c ContactResolver) PollerOption {
	return func(p *Poller) { p.contacts = c }
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
func NewPoller(manager *Manager, state *opstate.Store, logger *slog.Logger, opts ...PollerOption) *Poller {
	if logger == nil {
		logger = slog.Default()
	}
	p := &Poller{
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

// CheckNewMessages polls every configured account for messages newer
// than its stored high-water mark and dispatches each new message as
// a [messages.LoopEventPayload] to the configured wake_loop target
// (default: [DefaultHandlerLoopName]). Each envelope carries the
// per-message trust-zone tag derived from the sender via the
// configured contact resolver, so the receiving loop's iteration sees
// "owner" / "trusted" / "household" / "known" / "stranger" in its
// Request.InitialTags and can route the triage accordingly.
//
// On first run (no stored high-water mark), the current highest UID
// is recorded silently without reporting it as new — this prevents
// flooding the agent with the entire inbox on initial deployment.
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

	stateKey := accountName + ":INBOX"

	storedStr, err := p.state.Get(pollNamespace, stateKey)
	if err != nil {
		return 0, 0, fmt.Errorf("get high-water mark %q: %w", stateKey, err)
	}

	var storedUID uint64
	switch storedStr {
	case "":
		p.logger.Debug("email poll first run for account", "account", accountName)
		envelopes, err := client.ListMessages(ctx, ListOptions{Folder: "INBOX", Limit: 1})
		if err != nil {
			return 0, 0, fmt.Errorf("seed list %q: %w", accountName, err)
		}
		if len(envelopes) == 0 {
			return 0, 0, nil
		}
		seedUID := envelopes[0].UID
		p.logger.Info("email poll first run, seeding high-water mark",
			"account", accountName,
			"uid", seedUID,
		)
		if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(seedUID), 10)); err != nil {
			return 0, 0, fmt.Errorf("seed high-water mark %q: %w", stateKey, err)
		}
		return 0, 0, nil

	default:
		parsed, err := strconv.ParseUint(storedStr, 10, 32)
		if err != nil {
			p.logger.Warn("corrupt high-water mark, reseeding",
				"account", accountName,
				"stored", storedStr,
			)
			envelopes, err := client.ListMessages(ctx, ListOptions{Folder: "INBOX", Limit: 1})
			if err != nil {
				return 0, 0, fmt.Errorf("reseed list %q: %w", accountName, err)
			}
			if len(envelopes) > 0 {
				if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(envelopes[0].UID), 10)); err != nil {
					return 0, 0, fmt.Errorf("reseed high-water mark %q: %w", stateKey, err)
				}
			}
			return 0, 0, nil
		}
		storedUID = parsed
	}

	p.logger.Debug("email poll querying IMAP",
		"account", accountName,
		"since_uid", storedUID,
	)

	newMessages, err := client.ListMessages(ctx, ListOptions{
		Folder:   "INBOX",
		SinceUID: uint32(storedUID),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list messages %q: %w", accountName, err)
	}

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
	var overallMaxUID uint64
	for _, env := range newMessages {
		if uint64(env.UID) > overallMaxUID {
			overallMaxUID = uint64(env.UID)
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

	delivered, err := p.dispatchAccountBatches(ctx, accountName, stateKey, storedUID, newMessages)
	if err != nil {
		// Partial progress is already persisted by dispatchAccountBatches
		// (per-batch high-water advance on success). The next poll picks
		// up from the last-successful UID.
		return preFilterCount, delivered, err
	}

	// All filtered messages delivered. Bump the high-water mark past
	// any self-sent UIDs above the final batch so the next poll
	// doesn't re-observe them. No-op when the dispatched batches
	// already covered overallMaxUID (the common case).
	if overallMaxUID > 0 {
		if err := p.setHighWaterMark(stateKey, overallMaxUID); err != nil {
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
// monotonically. Each batch's wake envelope carries the deduplicated
// union of sender-trust tags for the messages in that batch — a
// stranger-heavy batch followed by an owner batch gets distinct tags
// on each iteration, instead of always seeing the union across all
// senders.
//
// Returns the total number of events delivered across all successful
// batches. A nil message bus is a no-op (logs and returns 0) so a
// transient bus-missing window doesn't error.
func (p *Poller) dispatchAccountBatches(ctx context.Context, accountName, stateKey string, currentMark uint64, newMessages []Envelope) (int, error) {
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
		end := start + batchSize
		if end > len(ordered) {
			end = len(ordered)
		}
		chunk := ordered[start:end]
		events, tags, batchMaxUID := p.buildBatchEvents(accountName, chunk)

		target := p.wakeLoop
		target.Tags = mergeUniqueStrings(target.Tags, tags)
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

		// Persist per-batch progress only when this batch's max UID
		// actually exceeds the running mark — guards against
		// non-monotonic advancement if the slice ever arrives
		// reordered.
		if batchMaxUID > currentHigh {
			if err := p.setHighWaterMark(stateKey, batchMaxUID); err != nil {
				return delivered, err
			}
			currentHigh = batchMaxUID
		}
	}
	return delivered, nil
}

// buildBatchEvents converts a chunk of envelopes into structured
// LoopEventPayloads, returning the events, the deduplicated sender-tag
// set for the batch, and the highest UID observed in the chunk.
func (p *Poller) buildBatchEvents(accountName string, chunk []Envelope) ([]messages.LoopEventPayload, []string, uint64) {
	events := make([]messages.LoopEventPayload, 0, len(chunk))
	tagsSeen := make(map[string]struct{})
	var tags []string
	var maxUID uint64
	for _, env := range chunk {
		zone, _ := p.lookupTrustZone(env.From)
		tag := senderTag(zone)
		if _, dup := tagsSeen[tag]; !dup {
			tagsSeen[tag] = struct{}{}
			tags = append(tags, tag)
		}
		if uint64(env.UID) > maxUID {
			maxUID = uint64(env.UID)
		}
		events = append(events, messages.LoopEventPayload{
			Source:     "email_poll",
			Type:       "new_message",
			ID:         fmt.Sprintf("%s:%d", accountName, env.UID),
			Title:      env.Subject,
			Summary:    fmt.Sprintf("From %s in %s/INBOX", env.From, accountName),
			ObservedAt: env.Date,
			Metadata: map[string]string{
				"account":    accountName,
				"folder":     "INBOX",
				"uid":        strconv.FormatUint(uint64(env.UID), 10),
				"from":       env.From,
				"trust_zone": zone,
				"tag":        tag,
			},
		})
	}
	return events, tags, maxUID
}

// setHighWaterMark persists a UID to the per-account high-water key
// without consulting prior state. The caller is responsible for
// monotonicity; dispatchAccountBatches enforces that by tracking the
// running mark across batches.
func (p *Poller) setHighWaterMark(stateKey string, uid uint64) error {
	if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uid, 10)); err != nil {
		return fmt.Errorf("update high-water mark %q: %w", stateKey, err)
	}
	return nil
}

// lookupTrustZone returns the contact's trust zone for a sender. An
// unconfigured resolver, a missing contact, or a lookup error all
// return ("", false) — the caller maps that to the "stranger" tag.
func (p *Poller) lookupTrustZone(from string) (string, bool) {
	if p.contacts == nil {
		return "", false
	}
	addr := strings.ToLower(extractAddress(from))
	if addr == "" {
		return "", false
	}
	zone, found, err := p.contacts.ResolveTrustZone(addr)
	if err != nil {
		p.logger.Warn("contact lookup failed for incoming email; treating as stranger",
			"from", from, "error", err)
		return "", false
	}
	if !found {
		return "", false
	}
	return zone, true
}

// senderTag maps a contacts trust zone to the iteration-scoped tag
// stamped on the wake envelope. Senders without a matching contact
// stamp "stranger" so the receiving loop can route triage by sender
// familiarity. Unknown / unrecognised zones fall back to "stranger"
// so a future zone added to the contacts model doesn't silently
// promote a sender to "trusted".
func senderTag(zone string) string {
	switch zone {
	case "admin":
		return "owner"
	case "household":
		return "household"
	case "trusted":
		return "trusted"
	case "known":
		return "known"
	default:
		return "stranger"
	}
}

// mergeUniqueStrings concatenates two string slices, dropping
// whitespace-only entries and preserving the first slice's order.
func mergeUniqueStrings(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, slice := range [][]string{base, extra} {
		for _, s := range slice {
			t := strings.TrimSpace(s)
			if t == "" {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// filterSelfSent removes messages where From matches the account's
// default_from address. This prevents the agent from triaging its own
// outbound replies that appear in INBOX (Bcc-to-self, server-side copies).
func (p *Poller) filterSelfSent(accountName string, messages []Envelope) []Envelope {
	acctCfg, err := p.manager.AccountConfig(accountName)
	if err != nil || acctCfg.DefaultFrom == "" {
		return messages // can't filter without a configured From address
	}

	ownAddr := strings.ToLower(extractAddress(acctCfg.DefaultFrom))
	filtered := make([]Envelope, 0, len(messages))
	for _, env := range messages {
		fromAddr := strings.ToLower(extractAddress(env.From))
		if fromAddr == ownAddr {
			p.logger.Debug("skipping self-sent message",
				"account", accountName,
				"uid", env.UID,
				"subject", env.Subject,
			)
			continue
		}
		filtered = append(filtered, env)
	}
	return filtered
}

// advanceHighWaterMark updates the stored high-water mark to the highest
// UID found in the result set, but never decreases it. The function
// scans all messages to determine the maximum UID rather than relying
// on any particular ordering of the input slice.
func (p *Poller) advanceHighWaterMark(accountName, stateKey string, currentMark uint64, allNew []Envelope) error {
	// Find the highest UID across all fetched messages (including
	// self-sent ones that will be filtered later). We scan all rather
	// than trusting sort order as a defensive measure.
	var highest uint64
	for _, env := range allNew {
		if uint64(env.UID) > highest {
			highest = uint64(env.UID)
		}
	}

	// Never decrease — UIDs can disappear when messages are moved/deleted
	// but the mark must only advance.
	if highest <= currentMark {
		return nil
	}

	p.logger.Debug("advancing high-water mark",
		"account", accountName,
		"old_uid", currentMark,
		"new_uid", highest,
	)

	if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(highest, 10)); err != nil {
		return fmt.Errorf("update high-water mark %q: %w", stateKey, err)
	}
	return nil
}
