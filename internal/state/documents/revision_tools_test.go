package documents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type stubReviser struct {
	listing RevisionListing
	diff    RevisionDiff
	content RevisionContent
}

func (s stubReviser) Resolve(context.Context, string, string) (RevisionRef, error) {
	return s.content.Revision, nil
}
func (s stubReviser) History(context.Context, string, RevisionQuery) (RevisionListing, error) {
	return s.listing, nil
}
func (s stubReviser) Diff(context.Context, string, string, string, string) (RevisionDiff, error) {
	return s.diff, nil
}
func (s stubReviser) Content(context.Context, string, string) (RevisionContent, error) {
	return s.content, nil
}

func newRevisionTools(t *testing.T, roots map[string]string, revisers map[string]RootReviser) *Tools {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStoreWithOptions(db, roots, nil, StoreOptions{RootRevisers: revisers})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return NewTools(store)
}

func testRevisions() (RevisionRef, RevisionRef) {
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	agent := &RevisionSigner{Verified: true, Principal: "thane@provenance.local", Kind: "agent"}
	newest := RevisionRef{Commit: "aaaaaaaaaaaa", Short: "aaaaaaaaaaaa", Index: 0, Timestamp: ts, Message: "third", Signer: agent}
	older := RevisionRef{Commit: "bbbbbbbbbbbb", Short: "bbbbbbbbbbbb", Index: 1, Timestamp: ts.Add(-time.Hour), Message: "second", Signer: agent}
	return newest, older
}

func TestToolsHistory(t *testing.T) {
	newest, older := testRevisions()
	tools := newRevisionTools(t,
		map[string]string{"kb": t.TempDir()},
		map[string]RootReviser{"kb": stubReviser{listing: RevisionListing{
			Revisions: []RevisionRef{newest, older}, Total: 5, NextBefore: "bbbbbbbbbbbb",
		}}},
	)
	out, err := tools.History(context.Background(), HistoryArgs{Ref: "kb:doc.md"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var got modelHistory
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.RevisionCount != 5 || len(got.Revisions) != 2 || got.NextBefore != "bbbbbbbbbbbb" {
		t.Fatalf("history = count %d, %d revs, next %q; want 5/2/bbbb", got.RevisionCount, len(got.Revisions), got.NextBefore)
	}
	if got.Revisions[0].Rev != "aaaaaaaaaaaa" || got.Revisions[0].Index != 0 || got.Revisions[0].Age == "" {
		t.Fatalf("newest = %+v, want rev aaaa idx 0 with an age", got.Revisions[0])
	}
	if s := got.Revisions[0].Signer; s == nil || !s.Verified || s.Kind != "agent" {
		t.Fatalf("newest signer = %+v, want verified agent", s)
	}
}

func TestToolsDiff(t *testing.T) {
	newest, older := testRevisions()
	tools := newRevisionTools(t,
		map[string]string{"kb": t.TempDir()},
		map[string]RootReviser{"kb": stubReviser{diff: RevisionDiff{
			Base: older, Target: newest, Format: "patch", Added: 2, Removed: 0, Body: "+b\n+c\n",
		}}},
	)
	out, err := tools.Diff(context.Background(), DiffArgs{Ref: "kb:doc.md", From: "bbbbbbbbbbbb"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var got modelDiff
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.Base.Rev != "bbbbbbbbbbbb" || got.Target.Rev != "aaaaaaaaaaaa" {
		t.Fatalf("base/target = %q/%q, want bbbb/aaaa", got.Base.Rev, got.Target.Rev)
	}
	if got.Added != 2 || !strings.Contains(got.Patch, "+b") {
		t.Fatalf("diff = +%d, patch %q", got.Added, got.Patch)
	}
}

func TestToolsAt_TrustedAndUnverified(t *testing.T) {
	newest, _ := testRevisions()
	body := "---\ntitle: Doc\n---\nhello body\n"

	trusted := newRevisionTools(t,
		map[string]string{"kb": t.TempDir()},
		map[string]RootReviser{"kb": stubReviser{content: RevisionContent{Revision: newest, Content: body}}},
	)
	out, err := trusted.At(context.Background(), AtArgs{Ref: "kb:doc.md"})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	var got modelRevisionAt
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.VerifyStatus != "trusted" || !strings.Contains(got.Body, "hello body") || got.UnverifiedBody != "" {
		t.Fatalf("trusted at = status %q, body %q, unverified %q", got.VerifyStatus, got.Body, got.UnverifiedBody)
	}

	// An untrusted revision ships its content under unverified_body.
	untrustedRev := newest
	untrustedRev.Signer = &RevisionSigner{Verified: false, Kind: "unknown", Reason: "unsigned"}
	untrusted := newRevisionTools(t,
		map[string]string{"kb": t.TempDir()},
		map[string]RootReviser{"kb": stubReviser{content: RevisionContent{Revision: untrustedRev, Content: body}}},
	)
	out2, err := untrusted.At(context.Background(), AtArgs{Ref: "kb:doc.md"})
	if err != nil {
		t.Fatalf("At untrusted: %v", err)
	}
	var got2 modelRevisionAt
	if err := json.Unmarshal([]byte(out2), &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got2.VerifyStatus != "unverified" || got2.Body != "" || !strings.Contains(got2.UnverifiedBody, "hello body") {
		t.Fatalf("untrusted at = status %q, body %q, unverified %q", got2.VerifyStatus, got2.Body, got2.UnverifiedBody)
	}
}

func TestToolsRevisionErrors(t *testing.T) {
	tools := newRevisionTools(t,
		map[string]string{"kb": t.TempDir(), "plain": t.TempDir()},
		map[string]RootReviser{"kb": stubReviser{}},
	)
	ctx := context.Background()

	if _, err := tools.History(ctx, HistoryArgs{Ref: ""}); err == nil {
		t.Fatal("empty ref accepted")
	}
	if _, err := tools.History(ctx, HistoryArgs{Ref: "ghost:doc.md"}); err == nil || !strings.Contains(err.Error(), "unknown root") {
		t.Fatalf("unknown root err = %v, want 'unknown root'", err)
	}
	if _, err := tools.History(ctx, HistoryArgs{Ref: "plain:doc.md"}); err == nil || !strings.Contains(err.Error(), "not git-backed") {
		t.Fatalf("non-git root err = %v, want 'not git-backed'", err)
	}
	if _, err := tools.Diff(ctx, DiffArgs{Ref: "kb:doc.md"}); err == nil || !strings.Contains(err.Error(), "from is required") {
		t.Fatalf("missing from err = %v, want 'from is required'", err)
	}
}

func TestNormalizeSelector(t *testing.T) {
	tools := newRevisionTools(t, map[string]string{"kb": t.TempDir()}, map[string]RootReviser{"kb": stubReviser{}})
	if got := tools.normalizeSelector("HEAD"); got != "HEAD" {
		t.Fatalf("HEAD = %q, want HEAD", got)
	}
	if got := tools.normalizeSelector("a1b2c3def456"); got != "a1b2c3def456" {
		t.Fatalf("hash = %q, want unchanged", got)
	}
	if got := tools.normalizeSelector("-3600s"); !strings.Contains(got, "T") || strings.HasPrefix(got, "-") {
		t.Fatalf("delta = %q, want an RFC3339 timestamp", got)
	}
}
