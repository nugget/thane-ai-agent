package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// MarkMessages adds or removes a flag on the specified messages.
// The flag must be one of "seen", "flagged", or "answered".
func (c *Client) MarkMessages(ctx context.Context, action MarkAction) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return err
	}

	if len(action.UIDs) == 0 {
		return fmt.Errorf("no UIDs specified")
	}

	imapFlag, ok := ValidFlag(action.Flag)
	if !ok {
		return fmt.Errorf("invalid flag %q (valid: seen, flagged, answered)", action.Flag)
	}

	folder := action.Folder
	if folder == "" {
		folder = "INBOX"
	}

	if _, err := c.selectFolder(folder); err != nil {
		return err
	}

	uidSet := imap.UIDSet{}
	for _, uid := range action.UIDs {
		uidSet.AddNum(imap.UID(uid))
	}

	op := imap.StoreFlagsAdd
	if !action.Add {
		op = imap.StoreFlagsDel
	}

	storeCmd := c.client.Store(uidSet, &imap.StoreFlags{
		Op:     op,
		Silent: true,
		Flags:  []imap.Flag{imap.Flag(imapFlag)},
	}, nil)

	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("store flags: %w", err)
	}

	return nil
}
