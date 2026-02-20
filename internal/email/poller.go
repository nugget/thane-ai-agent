package email

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/opstate"
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
	var sections []string

	for _, name := range p.manager.AccountNames() {
		section, err := p.checkAccount(ctx, name)
		if err != nil {
			p.logger.Warn("email poll failed for account",
				"account", name,
				"error", err,
			)
			continue
		}
		if section != "" {
			sections = append(sections, section)
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
// Returns a formatted section for the wake message, or empty string
// if no new messages were found.
func (p *Poller) checkAccount(ctx context.Context, accountName string) (string, error) {
	client, err := p.manager.Account(accountName)
	if err != nil {
		return "", fmt.Errorf("get account %q: %w", accountName, err)
	}

	stateKey := accountName + ":INBOX"

	// Fetch recent messages (not just unseen — we track by UID, not flags).
	envelopes, err := client.ListMessages(ctx, ListOptions{
		Folder: "INBOX",
		Limit:  50,
	})
	if err != nil {
		return "", fmt.Errorf("list messages %q: %w", accountName, err)
	}

	if len(envelopes) == 0 {
		return "", nil
	}

	// Find the highest UID in this batch (envelopes are newest-first).
	highestUID := envelopes[0].UID

	// Load the stored high-water mark.
	storedStr, err := p.state.Get(pollNamespace, stateKey)
	if err != nil {
		return "", fmt.Errorf("get high-water mark %q: %w", stateKey, err)
	}

	// First run: seed the high-water mark without reporting.
	if storedStr == "" {
		p.logger.Info("email poll first run, seeding high-water mark",
			"account", accountName,
			"uid", highestUID,
		)
		if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(highestUID), 10)); err != nil {
			return "", fmt.Errorf("seed high-water mark %q: %w", stateKey, err)
		}
		return "", nil
	}

	storedUID, err := strconv.ParseUint(storedStr, 10, 32)
	if err != nil {
		// Corrupted state — reseed.
		p.logger.Warn("corrupt high-water mark, reseeding",
			"account", accountName,
			"stored", storedStr,
		)
		if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(highestUID), 10)); err != nil {
			return "", fmt.Errorf("reseed high-water mark %q: %w", stateKey, err)
		}
		return "", nil
	}

	// Collect messages newer than the high-water mark.
	var newMessages []Envelope
	for _, env := range envelopes {
		if uint64(env.UID) > storedUID {
			newMessages = append(newMessages, env)
		}
	}

	if len(newMessages) == 0 {
		return "", nil
	}

	// Update the high-water mark.
	if err := p.state.Set(pollNamespace, stateKey, strconv.FormatUint(uint64(highestUID), 10)); err != nil {
		return "", fmt.Errorf("update high-water mark %q: %w", stateKey, err)
	}

	// Format the wake section for this account.
	return formatPollSection(accountName, newMessages), nil
}

// formatPollSection builds a wake message section for new messages on
// a single account.
func formatPollSection(accountName string, messages []Envelope) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Account: %s (INBOX)\n", accountName))

	for _, env := range messages {
		sb.WriteString(fmt.Sprintf("  From: %s\n", env.From))
		sb.WriteString(fmt.Sprintf("  Subject: %s\n", env.Subject))
		sb.WriteString(fmt.Sprintf("  Date: %s\n", env.Date.Format("2006-01-02 15:04")))
		sb.WriteString("\n")
	}

	return sb.String()
}
