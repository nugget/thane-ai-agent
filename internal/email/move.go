package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// MoveMessages moves the specified messages from the source folder to
// the destination folder. Uses the IMAP MOVE extension when available,
// falling back to COPY + STORE \Deleted + EXPUNGE automatically.
func (c *Client) MoveMessages(ctx context.Context, opts MoveOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return err
	}

	if len(opts.UIDs) == 0 {
		return fmt.Errorf("no UIDs specified")
	}

	folder := opts.Folder
	if folder == "" {
		folder = "INBOX"
	}

	if opts.Destination == "" {
		return fmt.Errorf("destination folder is required")
	}

	if _, err := c.selectFolder(folder); err != nil {
		return err
	}

	uidSet := imap.UIDSet{}
	for _, uid := range opts.UIDs {
		uidSet.AddNum(imap.UID(uid))
	}

	// Move handles MOVE extension with COPY+DELETE+EXPUNGE fallback.
	moveCmd := c.client.Move(uidSet, opts.Destination)
	if _, err := moveCmd.Wait(); err != nil {
		return fmt.Errorf("move to %s: %w", opts.Destination, err)
	}

	return nil
}
