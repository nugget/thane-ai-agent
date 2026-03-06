package email

import (
	"context"
	"fmt"
	"sort"

	"github.com/emersion/go-imap/v2"
)

// ListFolders returns all mailboxes for the account with their message
// and unseen counts. Results are sorted alphabetically by name.
func (c *Client) ListFolders(ctx context.Context) ([]Folder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}

	// List all mailboxes.
	listCmd := c.client.List("", "*", nil)
	mailboxes, err := listCmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}

	var folders []Folder
	for _, mbox := range mailboxes {
		name := mbox.Mailbox

		// Skip non-selectable mailboxes.
		hasNoselect := false
		attrs := make([]string, 0, len(mbox.Attrs))
		for _, attr := range mbox.Attrs {
			s := string(attr)
			attrs = append(attrs, s)
			if attr == imap.MailboxAttrNoSelect {
				hasNoselect = true
			}
		}

		folder := Folder{
			Name:       name,
			Attributes: attrs,
		}

		// Get message counts for selectable mailboxes.
		if !hasNoselect {
			statusOpts := &imap.StatusOptions{
				NumMessages: true,
				NumUnseen:   true,
			}
			statusCmd := c.client.Status(name, statusOpts)
			statusData, err := statusCmd.Wait()
			if err != nil {
				c.logger.Debug("status failed for mailbox", "mailbox", name, "error", err)
			} else {
				if statusData.NumMessages != nil {
					folder.Messages = *statusData.NumMessages
				}
				if statusData.NumUnseen != nil {
					folder.Unseen = *statusData.NumUnseen
				}
			}
		}

		folders = append(folders, folder)
	}

	sort.Slice(folders, func(i, j int) bool {
		return folders[i].Name < folders[j].Name
	})

	return folders, nil
}
