package email

import (
	"context"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// Every read-write SELECT tells the client which flags stick in that
// folder (PERMANENTFLAGS). selectFolder records the verdict per folder,
// so the Email Accounts entry can show it without touching the server,
// and a label write reads it from the SELECT it made itself.

// folderVerdict is what the last read-write SELECT of one folder said
// about keywords, and whether the poller has logged that it cannot keep
// them.
type folderVerdict struct {
	verdict KeywordVerdict
	warned  bool
}

// verdictKey is a folder's key in the verdict cache. INBOX is
// case-insensitive (RFC 3501); every other name is kept as spelled.
func verdictKey(folder string) string {
	folder = normalizeFolder(folder)
	if strings.EqualFold(folder, DefaultFolder) {
		return DefaultFolder
	}
	return folder
}

// noteKeywordVerdict records a read-write SELECT's PERMANENTFLAGS.
func (c *Client) noteKeywordVerdict(folder string, permanent []imap.Flag) {
	c.verdictMu.Lock()
	defer c.verdictMu.Unlock()
	if c.verdicts == nil {
		c.verdicts = make(map[string]folderVerdict)
	}
	key := verdictKey(folder)
	v := c.verdicts[key]
	v.verdict = keywordVerdictOf(permanent)
	c.verdicts[key] = v
}

// keywordVerdict returns a folder's last recorded verdict, or ok=false
// when no read-write SELECT of it has succeeded yet.
func (c *Client) keywordVerdict(folder string) (KeywordVerdict, bool) {
	c.verdictMu.Lock()
	defer c.verdictMu.Unlock()
	v, ok := c.verdicts[verdictKey(folder)]
	return v.verdict, ok
}

// claimVerdictWarning reports true the first time it is called for a
// folder and false after, so a folder that cannot keep keywords is
// logged once, not on every poll.
func (c *Client) claimVerdictWarning(folder string) bool {
	c.verdictMu.Lock()
	defer c.verdictMu.Unlock()
	if c.verdicts == nil {
		c.verdicts = make(map[string]folderVerdict)
	}
	key := verdictKey(folder)
	v := c.verdicts[key]
	if v.warned {
		return false
	}
	v.warned = true
	c.verdicts[key] = v
	return true
}

// flagSession is one folder selected read-write inside one locked client
// operation, so reading a message's flags and changing them happen with
// no other Thane operation in between.
type flagSession struct {
	c         *Client
	ctx       context.Context
	folder    string
	verdict   KeywordVerdict
	permanent []imap.Flag

	// uidValidity is the folder's UIDVALIDITY as the SELECT reported it,
	// which with a UID names the copy a label record describes (copyOf).
	uidValidity uint32
}

// withFlagSession selects folder read-write and runs fn inside the
// client's lock. Selecting and fetching flags never marks mail seen.
func (c *Client) withFlagSession(ctx context.Context, folder string, fn func(*flagSession) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return err
	}
	folder = normalizeFolder(folder)
	data, err := c.selectFolder(ctx, folder, false)
	if err != nil {
		return err
	}
	return fn(&flagSession{
		c:           c,
		ctx:         ctx,
		folder:      folder,
		verdict:     keywordVerdictOf(data.PermanentFlags),
		permanent:   slices.Clone(data.PermanentFlags),
		uidValidity: data.UIDValidity,
	})
}

// flagState is one message's Message-ID and current flags.
type flagState struct {
	MessageID string
	Flags     []string
}

// states fetches the Message-ID and flags of every UID still in the
// folder; a UID the folder does not hold is absent from the result.
func (s *flagSession) states(uids []uint32) (map[uint32]*flagState, error) {
	out := make(map[uint32]*flagState, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	set := make([]imap.UID, len(uids))
	for i, uid := range uids {
		set[i] = imap.UID(uid)
	}
	envs, err := s.c.fetchEnvelopes(s.ctx, s.folder, set)
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		out[env.UID] = &flagState{MessageID: env.MessageID, Flags: slices.Clone(env.Flags)}
	}
	return out, nil
}

// store adds or removes flags on one message with a silent UID STORE.
// Nothing ever replaces a message's flags wholesale: a replacing STORE
// would erase marks the operator set.
func (s *flagSession) store(uid uint32, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if len(flags) == 0 {
		return nil
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	release := s.c.guard(s.ctx)
	err := s.c.client.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close()
	release()
	if err != nil {
		return s.c.wrap(s.ctx, "store flags", s.folder, uid, err)
	}
	return nil
}

// permanentList spells the folder's PERMANENTFLAGS the way the server
// sent them, for a refusal that quotes the server.
func (s *flagSession) permanentList() string {
	return "(" + strings.Join(flagNames(s.permanent), " ") + ")"
}

// applyStore updates a flagState after a successful STORE, so the next
// decision in the same session reads the message as it now is.
func (st *flagState) applyStore(op imap.StoreFlagsOp, flags []imap.Flag) {
	for _, f := range flags {
		has := stringFlagIn(st.Flags, string(f))
		switch {
		case op == imap.StoreFlagsAdd && !has:
			st.Flags = append(st.Flags, string(f))
		case op == imap.StoreFlagsDel && has:
			st.Flags = slices.DeleteFunc(st.Flags, func(x string) bool { return strings.EqualFold(x, string(f)) })
		}
	}
}
