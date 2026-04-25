package email

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const (
	// pollNamespace is the opstate namespace for email polling state.
	pollNamespace = "email_poll"
)

// Poller checks configured email accounts for new messages by comparing
// IMAP UIDs against a persisted high-water mark. It is not a tool — it
// runs as infrastructure code called by the scheduler task executor.
type Poller struct {
	manager *Manager
	state   *opstate.Store
	logger  *slog.Logger
}

// NewPoller creates an email poller that checks all accounts managed by
// the given Manager and tracks state in the provided opstate store.
func NewPoller(manager *Manager, state *opstate.Store, logger *slog.Logger) *Poller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		manager: manager,
		state:   state,
		logger:  logger,
	}
}

// CheckNewMessages checks all configured accounts for messages newer than
// the stored high-water mark. Returns a formatted wake message describing
// new messages, or empty string if nothing new was found.
//
// On first run (no stored high-water mark), the current highest UID is
// recorded silently without reporting it as new — this prevents flooding
// the agent with the entire inbox on initial deployment.
//
// Network errors are logged and skipped per-account; a failure on one
// account does not prevent checking others.
func (p *Poller) CheckNewMessages(ctx context.Context) (string, error) {
	accounts := p.manager.AccountNames()
	p.logger.Debug("email poll starting", "accounts", len(accounts))

	var sections []string
	var failed int
	var totalNew int

	for _, name := range accounts {
		p.logger.Debug("email poll checking account", "account", name)

		section, count, err := p.checkAccount(ctx, name)
		if err != nil {
			failed++
			p.logger.Warn("email poll failed for account",
				"account", name,
				"error", err,
			)
			continue
		}
		totalNew += count
		if section != "" {
			sections = append(sections, section)
		}
	}

	p.logger.Debug("email poll complete",
		"accounts", len(accounts),
		"accounts_with_new", len(sections),
		"failed", failed,
	)

	// Report iteration summary stats for the loop dashboard.
	if summary := loop.IterationSummary(ctx); summary != nil {
		summary["accounts_checked"] = len(accounts)
		summary["new_messages"] = totalNew
		if failed > 0 {
			summary["failed"] = failed
		}
	}

	if len(sections) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("New email detected:\n")
	for _, s := range sections {
		sb.WriteString("\n")
		sb.WriteString(s)
	}
	return sb.String(), nil
}

// checkAccount checks a single account's INBOX for new messages.
// Returns a formatted section for the wake message (or empty string if
// nothing new), the count of new messages found, and any error.
func (p *Poller) checkAccount(ctx context.Context, accountName string) (string, int, error) {
	client, err := p.manager.Account(accountName)
	if err != nil {
		return "", 0, fmt.Errorf("get account %q: %w", accountName, err)
	}

	stateKey := accountName + ":INBOX"

	// Load the stored high-water mark.
	storedStr, err := p.state.Get(pollNamespace, stateKey)
	if err != nil {
		return "", 0, fmt.Errorf("get high-water mark %q: %w", stateKey, err)
	}

	var storedUID uint64
	switch storedStr {
	case "":
		p.logger.Debug("email poll first run for account", "account", accountName)
		// First run: fetch recent messages to seed the high-water mark.
		envelopes, err := client.ListMessages(ctx, ListOptions{
			Folder: "INBOX",
			Limit:  1,
		})
		if err != nil {
			return "", 0, fmt.Errorf("seed list %q: %w", accountName, err)
		}
		if len(envelopes) == 0 {
			return "", 0, nil // empty mailbox, nothing to seed
		}
		seedUID := envelopes[0].UID
		p.logger.Info("email poll first run, seeding high-water mark",
			"account", accountName,
			"uid", seedUID,
		)
		if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(seedUID), 10)); err != nil {
			return "", 0, fmt.Errorf("seed high-water mark %q: %w", stateKey, err)
		}
		return "", 0, nil

	default:
		parsed, err := strconv.ParseUint(storedStr, 10, 32)
		if err != nil {
			// Corrupted state — reseed using recent messages.
			p.logger.Warn("corrupt high-water mark, reseeding",
				"account", accountName,
				"stored", storedStr,
			)
			envelopes, err := client.ListMessages(ctx, ListOptions{
				Folder: "INBOX",
				Limit:  1,
			})
			if err != nil {
				return "", 0, fmt.Errorf("reseed list %q: %w", accountName, err)
			}
			if len(envelopes) > 0 {
				if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(envelopes[0].UID), 10)); err != nil {
					return "", 0, fmt.Errorf("reseed high-water mark %q: %w", stateKey, err)
				}
			}
			return "", 0, nil
		}
		storedUID = parsed
	}

	p.logger.Debug("email poll querying IMAP",
		"account", accountName,
		"since_uid", storedUID,
	)

	// Fetch all messages with UIDs > storedUID (no limit — we want
	// every new message regardless of how many arrived between polls).
	newMessages, err := client.ListMessages(ctx, ListOptions{
		Folder:   "INBOX",
		SinceUID: uint32(storedUID),
	})
	if err != nil {
		return "", 0, fmt.Errorf("list messages %q: %w", accountName, err)
	}

	p.logger.Debug("email poll IMAP results",
		"account", accountName,
		"new_messages", len(newMessages),
	)

	if len(newMessages) == 0 {
		return "", 0, nil
	}

	// Always advance the high-water mark based on ALL fetched messages
	// (before filtering) so self-sent messages don't re-appear.
	if err := p.advanceHighWaterMark(accountName, stateKey, storedUID, newMessages); err != nil {
		return "", 0, err
	}

	// Filter out self-sent messages so the agent doesn't triage its
	// own replies. Compare the From address against the account's
	// configured default_from (if SMTP is set up).
	preFilterCount := len(newMessages)
	newMessages = p.filterSelfSent(accountName, newMessages)
	if preFilterCount != len(newMessages) {
		p.logger.Debug("email poll filtered self-sent messages",
			"account", accountName,
			"before", preFilterCount,
			"after", len(newMessages),
		)
	}
	if len(newMessages) == 0 {
		return "", 0, nil
	}

	p.logger.Debug("email poll account done",
		"account", accountName,
		"new_messages", len(newMessages),
	)

	return formatPollSection(accountName, newMessages), len(newMessages), nil
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

// formatPollSection builds a wake message section for new messages on
// a single account.
func formatPollSection(accountName string, messages []Envelope) string {
	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Account: %s (INBOX)\n", accountName))

	for _, env := range messages {
		sb.WriteString(fmt.Sprintf("  From: %s\n", env.From))
		sb.WriteString(fmt.Sprintf("  Subject: %s\n", env.Subject))
		sb.WriteString(fmt.Sprintf("  Date: %s\n", promptfmt.FormatDelta(env.Date, now)))
		sb.WriteString("\n")
	}

	return sb.String()
}
