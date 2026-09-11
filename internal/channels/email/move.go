package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// MoveMessages moves the given messages to another folder in the same
// account, using MOVE when the server has it and COPY + STORE \Deleted
// + UID EXPUNGE otherwise. A server with neither MOVE nor UIDPLUS is
// refused with [FailureUnsupported]: go-imap's fallback there would be
// a plain EXPUNGE, which removes every message anyone had flagged
// \Deleted in the source folder, not just the ones being moved. The
// result reports the new UIDs in the destination when the server
// returned COPYUID; a destination the account lacks is
// [FailureFolderNotFound] with the real folder list attached.
func (c *Client) MoveMessages(ctx context.Context, opts MoveOptions) (MoveResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return MoveResult{}, err
	}

	source := normalizeFolder(opts.Folder)
	result := MoveResult{SourceFolder: source, Destination: opts.Destination, UIDs: opts.UIDs}

	if len(opts.UIDs) == 0 {
		return result, fmt.Errorf("no UIDs specified")
	}
	if opts.Destination == "" {
		return result, fmt.Errorf("destination folder is required")
	}

	release := c.guard(ctx)
	caps := c.client.Caps()
	release()
	if !caps.Has(imap.CapMove) && !caps.Has(imap.CapUIDPlus) {
		return result, &ClientError{Account: c.name, Op: "move messages to folder", Folder: opts.Destination, Kind: FailureUnsupported,
			Err: fmt.Errorf("server advertises neither MOVE nor UIDPLUS, and moving without them would expunge every message flagged \\Deleted in %q", source)}
	}

	if _, err := c.selectFolder(ctx, source, false); err != nil {
		return result, err
	}

	uidSet := imap.UIDSet{}
	for _, uid := range opts.UIDs {
		uidSet.AddNum(imap.UID(uid))
	}

	release = c.guard(ctx)
	data, err := c.client.Move(uidSet, opts.Destination).Wait()
	release()
	if err != nil {
		return result, c.folderError(ctx, "move messages to folder", opts.Destination, err)
	}

	if data != nil {
		if dest, ok := data.DestUIDs.(imap.UIDSet); ok {
			if nums, complete := dest.Nums(); complete && len(nums) > 0 {
				result.DestUIDs = make([]uint32, len(nums))
				for i, uid := range nums {
					result.DestUIDs[i] = uint32(uid)
				}
				result.DestUIDValidity = data.UIDValidity
				result.DestUIDsKnown = true
			}
		}
	}
	return result, nil
}
