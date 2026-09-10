package email

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// MarkMessages adds or removes a flag on the given messages and
// reports which UIDs the server actually changed. After the STORE it
// fetches the flags of the requested UIDs back: a UID that no longer
// exists in the folder returns nothing and so appears in Requested but
// not Affected, instead of vanishing into a silent success. The
// read-back also makes the answer independent of whether the server
// echoes UIDs in STORE responses.
func (c *Client) MarkMessages(ctx context.Context, action MarkAction) (MarkResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return MarkResult{}, err
	}

	folder := normalizeFolder(action.Folder)
	result := MarkResult{Folder: folder, Flag: action.Flag, Added: action.Add, Requested: action.UIDs}

	if len(action.UIDs) == 0 {
		return result, fmt.Errorf("no UIDs specified")
	}
	imapFlag, ok := validFlags[action.Flag]
	if !ok {
		return result, fmt.Errorf("invalid flag %q (valid: %s)", action.Flag, strings.Join(ValidFlagNames(), ", "))
	}

	if _, err := c.selectFolder(ctx, folder, false); err != nil {
		return result, err
	}

	uidSet := imap.UIDSet{}
	for _, uid := range action.UIDs {
		uidSet.AddNum(imap.UID(uid))
	}

	op := imap.StoreFlagsAdd
	if !action.Add {
		op = imap.StoreFlagsDel
	}

	release := c.guard(ctx)
	defer release()

	storeCmd := c.client.Store(uidSet, &imap.StoreFlags{
		Op:     op,
		Silent: true,
		Flags:  []imap.Flag{imapFlag},
	}, nil)
	if err := storeCmd.Close(); err != nil {
		return result, c.wrap(ctx, "store flags", folder, 0, err)
	}

	flags, err := c.fetchFlags(uidSet)
	if err != nil {
		return result, c.wrap(ctx, "read back flags", folder, 0, err)
	}
	for _, uid := range action.UIDs {
		have, exists := flags[uid]
		if !exists {
			continue
		}
		if slices.Contains(have, imapFlag) == action.Add {
			result.Affected = append(result.Affected, uid)
		}
	}
	return result, nil
}

// fetchFlags returns the current flags of every UID in set that still
// exists in the selected folder. Caller must hold c.mu, have selected
// the folder, and hold an active guard.
func (c *Client) fetchFlags(set imap.UIDSet) (map[uint32][]imap.Flag, error) {
	fetchCmd := c.client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true})
	out := make(map[uint32][]imap.Flag)
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		var uid uint32
		var flags []imap.Flag
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch data := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = uint32(data.UID)
			case imapclient.FetchItemDataFlags:
				flags = data.Flags
			case imapclient.FetchItemDataBodySection:
				drainLiteral(data.Literal)
			}
		}
		if uid != 0 {
			out[uid] = flags
		}
	}
	if err := fetchCmd.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
