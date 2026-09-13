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

	// COPYUID pairs the source UIDs the server actually moved with their
	// new UIDs, so a requested UID the folder no longer held is absent
	// from both. Without it, which UIDs moved is unknown.
	if data != nil {
		src, srcOK := data.SourceUIDs.(imap.UIDSet)
		dest, destOK := data.DestUIDs.(imap.UIDSet)
		if srcOK && destOK {
			srcNums, srcComplete := src.Nums()
			destNums, destComplete := dest.Nums()
			if srcComplete && destComplete && len(destNums) > 0 && len(srcNums) == len(destNums) {
				result.UIDs = make([]uint32, len(srcNums))
				result.DestUIDs = make([]uint32, len(destNums))
				for i := range srcNums {
					result.UIDs[i] = uint32(srcNums[i])
					result.DestUIDs[i] = uint32(destNums[i])
				}
				result.DestUIDValidity = data.UIDValidity
				result.DestUIDsKnown = true
			}
		}
	}
	return result, nil
}
