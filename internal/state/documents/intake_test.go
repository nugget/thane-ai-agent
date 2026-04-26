package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestDocumentIntakeProposesCreateAndCommit(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, nil)
	ctx := context.Background()

	out, err := tools.Intake(ctx, IntakeArgs{
		Root:         "kb",
		Intent:       "create a durable note",
		Summary:      "Driveway gate maintenance observations and reset procedure.",
		DesiredTitle: "Driveway Gate Notes",
		Tags:         []string{"Home Assistant"},
		BodySnippet:  "# Driveway Gate Notes\n\nThe driveway gate sometimes needs a controller reset.",
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	var intake IntakeResult
	if err := json.Unmarshal([]byte(out), &intake); err != nil {
		t.Fatalf("unmarshal intake: %v", err)
	}
	if intake.Status != IntakeReady || intake.RecommendedAction != IntakeActionCreateNew {
		t.Fatalf("intake status/action = %s/%s, want ready/create_new", intake.Status, intake.RecommendedAction)
	}
	if intake.IntakeID == "" || intake.ProposedRef != "kb:home-assistant/driveway-gate-notes.md" {
		t.Fatalf("intake id/ref = %q/%q, want id and canonical ref", intake.IntakeID, intake.ProposedRef)
	}

	commitOut, err := tools.Commit(ctx, CommitArgs{
		IntakeID: intake.IntakeID,
		Body:     "# Driveway Gate Notes\n\nThe reset process lives here.",
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var commit CommitResult
	if err := json.Unmarshal([]byte(commitOut), &commit); err != nil {
		t.Fatalf("unmarshal commit: %v", err)
	}
	if commit.Ref != intake.ProposedRef || commit.Action != IntakeActionCreateNew {
		t.Fatalf("commit = %#v, want create at %s", commit, intake.ProposedRef)
	}
	doc, err := tools.store.Read(ctx, intake.ProposedRef)
	if err != nil {
		t.Fatalf("Read committed doc: %v", err)
	}
	if doc.Title != "Driveway Gate Notes" || !containsString(doc.Tags, "home-assistant") {
		t.Fatalf("doc title/tags = %q/%v, want normalized metadata", doc.Title, doc.Tags)
	}
}

func TestDocumentIntakeRequiresConfirmationForSimilarCorpus(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, nil)
	ctx := context.Background()
	body := "# VLAN Guide\n\nHome network VLAN layout for trusted, IoT, and camera segments."
	if _, err := tools.store.Write(ctx, WriteArgs{
		Ref:   "kb:network/vlans.md",
		Title: "VLAN Guide",
		Tags:  []string{"network", "vlans"},
		Body:  &body,
	}); err != nil {
		t.Fatalf("seed Write: %v", err)
	}

	out, err := tools.Intake(ctx, IntakeArgs{
		Root:         "kb",
		Intent:       "create a new note about the home network VLAN layout",
		Summary:      "Home network VLAN layout for trusted IoT and camera segments.",
		DesiredTitle: "VLAN Guide",
		Tags:         []string{"network", "vlans"},
		BodySnippet:  body,
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	var intake IntakeResult
	if err := json.Unmarshal([]byte(out), &intake); err != nil {
		t.Fatalf("unmarshal intake: %v", err)
	}
	if intake.Status != IntakeConfirmUpdate || intake.TargetRef != "kb:network/vlans.md" {
		t.Fatalf("intake status/target = %s/%q, want confirm_update existing VLAN doc", intake.Status, intake.TargetRef)
	}

	if _, err := tools.Commit(ctx, CommitArgs{
		IntakeID: intake.IntakeID,
		Action:   IntakeActionCreateNew,
		Body:     "# VLAN Follow-up\n\nA separate copy should require confirmation.",
	}); err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("Commit without confirm error = %v, want confirmation requirement", err)
	}

	if _, err := tools.Commit(ctx, CommitArgs{
		IntakeID: intake.IntakeID,
		Action:   IntakeActionUpdateExisting,
		Body:     "Additional VLAN operational detail.",
		Confirm:  true,
	}); err != nil {
		t.Fatalf("Commit update_existing: %v", err)
	}
	doc, err := tools.store.Read(ctx, "kb:network/vlans.md")
	if err != nil {
		t.Fatalf("Read updated doc: %v", err)
	}
	if !strings.Contains(doc.Body, "Additional VLAN operational detail.") {
		t.Fatalf("updated body = %q, want appended intake body", doc.Body)
	}
}

func TestDocumentIntakeRefusesReadOnlyRoot(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringReadOnly,
		},
	})
	out, err := tools.Intake(context.Background(), IntakeArgs{
		Root:         "kb",
		Summary:      "Read-only root should not accept managed intake writes.",
		DesiredTitle: "Read Only Intake",
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	var intake IntakeResult
	if err := json.Unmarshal([]byte(out), &intake); err != nil {
		t.Fatalf("unmarshal intake: %v", err)
	}
	if intake.Status != IntakeRefused || intake.RecommendedAction != IntakeActionDraftForReview {
		t.Fatalf("intake status/action = %s/%s, want refused/draft_for_review", intake.Status, intake.RecommendedAction)
	}
	if _, err := tools.Commit(context.Background(), CommitArgs{
		IntakeID: intake.IntakeID,
		Action:   IntakeActionCreateNew,
		Body:     "# Should Not Write\n",
		Confirm:  true,
	}); err == nil || !strings.Contains(err.Error(), "cannot be committed") {
		t.Fatalf("Commit read-only error = %v, want refusal", err)
	}
}

func TestDocumentCommitRejectsUnknownAction(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, nil)
	out, err := tools.Intake(context.Background(), IntakeArgs{
		Root:         "kb",
		Summary:      "A small note for action validation.",
		DesiredTitle: "Action Validation",
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	var intake IntakeResult
	if err := json.Unmarshal([]byte(out), &intake); err != nil {
		t.Fatalf("unmarshal intake: %v", err)
	}

	if _, err := tools.Commit(context.Background(), CommitArgs{
		IntakeID: intake.IntakeID,
		Action:   IntakeAction("creat_new"),
		Body:     "# Action Validation\n",
	}); err == nil || !strings.Contains(err.Error(), "unsupported action") {
		t.Fatalf("Commit unknown action error = %v, want unsupported action", err)
	}
}

func TestDocumentCommitDraftForReviewForgetsIntake(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, nil)
	out, err := tools.Intake(context.Background(), IntakeArgs{
		Root:         "kb",
		Summary:      "A draft-only intake should not stay resident.",
		DesiredTitle: "Draft Intake",
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	var intake IntakeResult
	if err := json.Unmarshal([]byte(out), &intake); err != nil {
		t.Fatalf("unmarshal intake: %v", err)
	}
	if _, err := tools.Commit(context.Background(), CommitArgs{
		IntakeID: intake.IntakeID,
		Action:   IntakeActionDraftForReview,
	}); err != nil {
		t.Fatalf("Commit draft_for_review: %v", err)
	}
	if _, err := tools.Commit(context.Background(), CommitArgs{
		IntakeID: intake.IntakeID,
		Body:     "# Draft Intake\n",
	}); err == nil || !strings.Contains(err.Error(), "unknown intake_id") {
		t.Fatalf("Commit reused draft intake error = %v, want unknown intake_id", err)
	}
}

func TestDocumentIntakeCacheIsBoundedAndExpires(t *testing.T) {
	t.Parallel()

	tools, _ := newIntakeTools(t, nil)
	for i := 0; i < maxIntakeEntries+10; i++ {
		result := &IntakeResult{
			Status:            IntakeReady,
			RecommendedAction: IntakeActionCreateNew,
			ProposedRef:       fmt.Sprintf("kb:notes/intake-%d.md", i),
		}
		tools.rememberIntake(result)
	}
	tools.intakeMu.Lock()
	got := len(tools.intakes)
	tools.intakeMu.Unlock()
	if got != maxIntakeEntries {
		t.Fatalf("intake cache size = %d, want %d", got, maxIntakeEntries)
	}

	tools.intakeMu.Lock()
	tools.intakes["old"] = intakeEntry{
		result: IntakeResult{
			IntakeID:          "old",
			Status:            IntakeReady,
			RecommendedAction: IntakeActionCreateNew,
		},
		createdAt: time.Now().Add(-intakeEntryTTL - time.Second),
	}
	tools.intakeMu.Unlock()

	if _, ok := tools.lookupIntake("old"); ok {
		t.Fatal("expired intake was still available")
	}
}

func TestNormalizeIntakeTagsDropsPunctuationOnlyTags(t *testing.T) {
	t.Parallel()

	got := normalizeIntakeTags([]string{"...", "--", "Home Assistant"}, []ValueCount{
		{Value: "home-assistant", Count: 2},
	})
	if len(got) != 1 || got[0] != "home-assistant" {
		t.Fatalf("normalizeIntakeTags = %#v, want only observed home-assistant tag", got)
	}
}

func newIntakeTools(t *testing.T, policies map[string]RootPolicy) (*Tools, string) {
	t.Helper()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewStoreWithOptions(db, map[string]string{"kb": kbDir}, nil, StoreOptions{
		RootPolicies: policies,
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return NewTools(store), kbDir
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
