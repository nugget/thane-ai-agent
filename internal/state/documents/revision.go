package documents

import (
	"context"
	"time"
)

// RootReviser exposes read-only revision history, diff, and point-in-time
// recall for a git-backed document root. It is the documents-layer,
// verifier-neutral counterpart to [RootWriter] and [RootVerifier]: the app
// adapts a git backend to it, so this package never depends on the git
// implementation.
//
// Filenames are root-relative; an implementation translates any repository
// prefix (when the git repo is a parent of the root) before querying.
// Selectors are resolved forms — "HEAD"/"latest", an RFC3339 timestamp, or a
// commit hash. Relative deltas are normalized to timestamps by the caller.
type RootReviser interface {
	// Resolve maps a selector onto a concrete revision of the file.
	Resolve(ctx context.Context, filename, selector string) (RevisionRef, error)
	// History returns a newest-first page of the file's revisions.
	History(ctx context.Context, filename string, opts RevisionQuery) (RevisionListing, error)
	// Diff returns the change to the file between two revisions.
	Diff(ctx context.Context, filename, from, to, format string) (RevisionDiff, error)
	// Content returns the file's content as of a revision, with that
	// revision's signer so the caller can gate on trust.
	Content(ctx context.Context, filename, selector string) (RevisionContent, error)
}

// Commit trailer names recorded on writes that pass through a git-backed
// document root. They are declared here, beside the revision types that
// project them, so the writer emitting a trailer and the reader naming it
// cannot drift apart.
//
// These describe the turn that produced a revision. History written by hand
// carries none of them, which is itself informative: a revision with no
// authorship block was not written by a loop.
const (
	TrailerModel        = "Thane-Model"
	TrailerLoopID       = "Thane-Loop-Id"
	TrailerConversation = "Thane-Conversation"
	TrailerSession      = "Thane-Session"
	TrailerRequest      = "Thane-Request"
	TrailerToolCall     = "Thane-Tool-Call"
	TrailerIteration    = "Thane-Iteration"
	// TrailerCoreHead pins the core root's HEAD at the moment of the write, so a
	// revision can be read beside the axioms, persona, mission, and talents that
	// were in force when it was made. Absent on core's own history, where the
	// parent commit already says it.
	TrailerCoreHead = "Thane-Core-Head"
	// TrailerVersion records the build that performed the write. Machine-stamped
	// so no document ever needs a hand-transcribed version claim — the first
	// production re-tag proved a model copying its own prior facet will carry a
	// retired label forward; the trailer is the fact, the facet is the judgment.
	TrailerVersion = "Thane-Version"
)

// RevisionQuery bounds a [RootReviser.History] page.
type RevisionQuery struct {
	// Limit caps the page size; non-positive means the implementation default.
	Limit int
	// Before is a pagination cursor (a commit hash); empty starts at HEAD.
	Before string
	// WithSigners populates each revision's Signer.
	WithSigners bool
}

// RevisionRef identifies one commit in a document's history.
type RevisionRef struct {
	// Commit is the full commit hash.
	Commit string
	// Short is the shortened hash used as the round-trippable token.
	Short string
	// Index is the count of revisions of this file newer than this one
	// (0 for the newest) — a reasoning aid, not an addressing token.
	Index int
	// Timestamp is the commit's author time.
	Timestamp time.Time
	// Message is the commit subject.
	Message string
	// Trailers are the commit's git trailers, keyed by trailer name. A write
	// made by a loop records the turn behind it here — which model, loop,
	// session, and request — so history can be attributed without joining
	// against the conversation store. Nil when the commit carries none, which
	// is the normal case for revisions written by hand.
	Trailers map[string]string
	// Signer describes who signed this revision, when populated.
	Signer *RevisionSigner
}

// RevisionSigner is the documents-layer, verifier-neutral form of a commit's
// signature attribution.
type RevisionSigner struct {
	Verified       bool
	Principal      string
	Kind           string
	KeyFingerprint string
	Reason         string
}

// RevisionListing is one page of a document's history, newest first.
type RevisionListing struct {
	Revisions  []RevisionRef
	Total      int
	NextBefore string
}

// RevisionDiff is the change to one document between two revisions.
type RevisionDiff struct {
	Base    RevisionRef
	Target  RevisionRef
	Format  string
	Added   int
	Removed int
	Body    string
}

// RevisionContent is a document's content as of a revision.
type RevisionContent struct {
	Revision RevisionRef
	Content  string
}
