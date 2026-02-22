package forge

import (
	"context"
	"strings"
	"testing"
	"time"
)

// mockProvider implements ForgeProvider for testing.
type mockProvider struct {
	issues  map[int]*Issue
	prs     map[int]*PullRequest
	diff    string
	reviews []*Review
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		issues: map[int]*Issue{
			1: {Number: 1, Title: "Bug report", Body: "It crashes", State: "open", URL: "https://example.com/1", Author: "alice", CreatedAt: time.Now()},
		},
		prs: map[int]*PullRequest{
			10: {Number: 10, Title: "Fix bug", State: "open", Head: "fix-branch", Base: "main"},
		},
		diff: "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
}

func (m *mockProvider) CreateIssue(_ context.Context, _ string, issue *Issue) (*Issue, error) {
	issue.Number = 99
	issue.URL = "https://example.com/99"
	return issue, nil
}
func (m *mockProvider) UpdateIssue(_ context.Context, _ string, number int, update *IssueUpdate) (*Issue, error) {
	i, ok := m.issues[number]
	if !ok {
		return nil, nil
	}
	if update.Title != nil {
		i.Title = *update.Title
	}
	if update.State != nil {
		i.State = *update.State
	}
	return i, nil
}
func (m *mockProvider) GetIssue(_ context.Context, _ string, number int) (*Issue, error) {
	i, ok := m.issues[number]
	if !ok {
		return nil, nil
	}
	return i, nil
}
func (m *mockProvider) ListIssues(_ context.Context, _ string, _ *ListOptions) ([]*Issue, error) {
	out := make([]*Issue, 0, len(m.issues))
	for _, i := range m.issues {
		out = append(out, i)
	}
	return out, nil
}
func (m *mockProvider) AddComment(_ context.Context, _ string, number int, body string) (*Comment, error) {
	return &Comment{ID: 1, Body: body, URL: "https://example.com/c1"}, nil
}
func (m *mockProvider) ListPRs(_ context.Context, _ string, _ *ListOptions) ([]*PullRequest, error) {
	out := make([]*PullRequest, 0, len(m.prs))
	for _, p := range m.prs {
		out = append(out, p)
	}
	return out, nil
}
func (m *mockProvider) GetPR(_ context.Context, _ string, number int) (*PullRequest, error) {
	p, _ := m.prs[number]
	return p, nil
}
func (m *mockProvider) GetPRFiles(_ context.Context, _ string, _ int) ([]*ChangedFile, error) {
	return []*ChangedFile{{Filename: "foo.go", Status: "modified", Additions: 1, Deletions: 1}}, nil
}
func (m *mockProvider) GetPRDiff(_ context.Context, _ string, _ int) (string, error) {
	return m.diff, nil
}
func (m *mockProvider) ListPRCommits(_ context.Context, _ string, _ int) ([]*Commit, error) {
	return nil, nil
}
func (m *mockProvider) ListPRReviews(_ context.Context, _ string, _ int) ([]*Review, error) {
	return m.reviews, nil
}
func (m *mockProvider) SubmitReview(_ context.Context, _ string, _ int, _ *Review) error {
	return nil
}
func (m *mockProvider) AddReviewComment(_ context.Context, _ string, _ int, _ *ReviewComment) error {
	return nil
}
func (m *mockProvider) ListChecks(_ context.Context, _ string, _ int) ([]*CheckRun, error) {
	return nil, nil
}
func (m *mockProvider) MergePR(_ context.Context, _ string, _ int, _ *MergeOptions) error {
	return nil
}
func (m *mockProvider) AddReaction(_ context.Context, _ string, _ int, _ int64, _ string) error {
	return nil
}
func (m *mockProvider) RequestReview(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}
func (m *mockProvider) Search(_ context.Context, _ string, _ SearchKind) ([]SearchResult, error) {
	return []SearchResult{{Kind: "issue", Number: 1, Title: "found", URL: "https://example.com/1"}}, nil
}

// testTools builds a Tools instance backed by the mock provider.
func testTools() *Tools {
	reg := &Registry{
		providers:   map[string]ForgeProvider{"mock": newMockProvider()},
		configs:     map[string]AccountConfig{"mock": {Name: "mock", Owner: "testowner"}},
		defaultName: "mock",
	}
	return NewTools(reg, nil)
}

func TestHandleIssueGet(t *testing.T) {
	tools := testTools()

	result, err := tools.HandleIssueGet(context.Background(), map[string]any{
		"repo":   "testowner/testrepo",
		"number": 1,
	})
	if err != nil {
		t.Fatalf("HandleIssueGet: %v", err)
	}
	if !strings.Contains(result, "Bug report") {
		t.Errorf("expected 'Bug report' in result, got: %s", result)
	}
}

func TestHandleIssueCreate_RequiredFields(t *testing.T) {
	tools := testTools()

	_, err := tools.HandleIssueCreate(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Errorf("expected 'repo is required' error, got %v", err)
	}

	_, err = tools.HandleIssueCreate(context.Background(), map[string]any{"repo": "owner/repo"})
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("expected 'title is required' error, got %v", err)
	}
}

func TestHandleIssueUpdate(t *testing.T) {
	tools := testTools()

	result, err := tools.HandleIssueUpdate(context.Background(), map[string]any{
		"repo":   "testowner/testrepo",
		"number": 1,
		"state":  "closed",
	})
	if err != nil {
		t.Fatalf("HandleIssueUpdate: %v", err)
	}
	if !strings.Contains(result, "#1") {
		t.Errorf("expected '#1' in result, got: %s", result)
	}
}

func TestHandlePRDiff_Truncation(t *testing.T) {
	tools := testTools()

	result, err := tools.HandlePRDiff(context.Background(), map[string]any{
		"repo":      "testowner/testrepo",
		"number":    10,
		"max_lines": 3,
	})
	if err != nil {
		t.Fatalf("HandlePRDiff: %v", err)
	}
	if !strings.Contains(result, "[diff truncated") {
		t.Errorf("expected truncation notice, got: %s", result)
	}
}

func TestHandlePRDiff_NoTruncation(t *testing.T) {
	tools := testTools()

	result, err := tools.HandlePRDiff(context.Background(), map[string]any{
		"repo":   "testowner/testrepo",
		"number": 10,
	})
	if err != nil {
		t.Fatalf("HandlePRDiff: %v", err)
	}
	if strings.Contains(result, "[diff truncated") {
		t.Errorf("unexpected truncation, got: %s", result)
	}
}
