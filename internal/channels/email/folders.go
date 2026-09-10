package email

import (
	"context"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// ListFolders returns every mailbox in the account with its role and
// counters, sorted by name. When the server supports LIST-EXTENDED,
// LIST-STATUS, and SPECIAL-USE the whole answer is one round trip;
// otherwise the counters come from one STATUS per selectable folder.
func (c *Client) ListFolders(ctx context.Context) ([]Folder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}

	caps := c.client.Caps()
	rev2 := caps.Has(imap.CapIMAP4rev2)
	extended := rev2 || caps.Has(imap.CapListExtended)
	listStatus := rev2 || caps.Has(imap.CapListStatus)
	specialUse := caps.Has(imap.CapSpecialUse)

	statusOpts := &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	var listOpts *imap.ListOptions
	if extended {
		listOpts = &imap.ListOptions{ReturnSpecialUse: specialUse}
		if listStatus {
			listOpts.ReturnStatus = statusOpts
		}
	}

	release := c.guard(ctx)
	mailboxes, err := c.client.List("", "*", listOpts).Collect()
	release()
	if err != nil {
		return nil, c.wrap(ctx, "list folders", "", 0, err)
	}

	folders := make([]Folder, 0, len(mailboxes))
	for _, mbox := range mailboxes {
		folder := Folder{
			Name:       mbox.Mailbox,
			Role:       roleFromAttrs(mbox.Attrs, mbox.Mailbox),
			Selectable: !hasAttr(mbox.Attrs, imap.MailboxAttrNoSelect) && !hasAttr(mbox.Attrs, imap.MailboxAttrNonExistent),
			Attributes: attrStrings(mbox.Attrs),
		}
		if mbox.Delim != 0 {
			folder.Delimiter = string(mbox.Delim)
		}

		switch {
		case !folder.Selectable:
			// Counters are meaningless for a container-only mailbox.
		case mbox.Status != nil:
			applyStatus(&folder, mbox.Status)
		default:
			release := c.guard(ctx)
			statusData, err := c.client.Status(mbox.Mailbox, statusOpts).Wait()
			release()
			if err != nil {
				c.logger.Debug("status failed for mailbox", "mailbox", mbox.Mailbox, "error", err)
			} else {
				applyStatus(&folder, statusData)
			}
		}
		folders = append(folders, folder)
	}

	slices.SortFunc(folders, func(a, b Folder) int {
		return strings.Compare(a.Name, b.Name)
	})
	return folders, nil
}

// applyStatus copies STATUS counters onto a folder.
func applyStatus(folder *Folder, status *imap.StatusData) {
	if status == nil {
		return
	}
	if status.NumMessages != nil {
		folder.Messages = *status.NumMessages
	}
	if status.NumUnseen != nil {
		folder.Unseen = *status.NumUnseen
	}
}

// attrStrings renders mailbox attributes as the server spells them.
func attrStrings(attrs []imap.MailboxAttr) []string {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = string(a)
	}
	return out
}

// roleFromAttrs derives a folder's special-use role. INBOX is a role by
// name because RFC 6154 defines no attribute for it.
func roleFromAttrs(attrs []imap.MailboxAttr, name string) FolderRole {
	for _, a := range attrs {
		switch a {
		case imap.MailboxAttrDrafts:
			return RoleDrafts
		case imap.MailboxAttrSent:
			return RoleSent
		case imap.MailboxAttrTrash:
			return RoleTrash
		case imap.MailboxAttrJunk:
			return RoleJunk
		case imap.MailboxAttrArchive:
			return RoleArchive
		case imap.MailboxAttrAll:
			return RoleAll
		case imap.MailboxAttrFlagged:
			return RoleFlagged
		case imap.MailboxAttrImportant:
			return RoleImportant
		}
	}
	if strings.EqualFold(name, DefaultFolder) {
		return RoleInbox
	}
	return ""
}
