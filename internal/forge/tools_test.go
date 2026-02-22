package forge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Mock provider ---

type mockProvider struct {
	name string

	// Return values for each method, set per-test.
	createIssueResult      *Issue
	createIssueErr         error
	updateIssueResult      *Issue
	updateIssueErr         error
	getIssueResult         *Issue
	getIssueErr            error
	listIssuesResult       []*Issue
	listIssuesErr          error
	addCommentResult       *Comment
	addCommentErr          error
	listPRsResult          []*PullRequest
	listPRsErr             error
	getPRResult            *PullRequest
	getPRErr               error
	getPRFilesResult       []*ChangedFile
	getPRFilesErr          error
	getPRDiffResult        string
	getPRDiffErr           error
	listPRCommitsResult    []*Commit
	listPRCommitsErr       error
	listPRReviewsResult    []*Review
	listPRReviewsErr       error
	submitReviewResult     *Review
	submitReviewErr        error
	addReviewCommentResult *ReviewComment
	addReviewCommentErr    error
	listChecksResult       []*CheckRun
	listChecksErr          error
	mergePRResult          *MergeResult
	mergePRErr             error
	addReactionErr         error
	requestReviewErr       error
	searchResult           []SearchResult
	searchErr              error

	// Call tracking.
	calls []mockCall
}

type mockCall struct {
	method string
	repo   string
	args   []any
}

func (m *mockProvider) record(method, repo string, args ...any) {
	m.calls = append(m.calls, mockCall{method: method, repo: repo, args: args})
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) CreateIssue(_ context.Context, repo string, issue *Issue) (*Issue, error) {
	m.record("CreateIssue", repo, issue)
	return m.createIssueResult, m.createIssueErr
}

func (m *mockProvider) UpdateIssue(_ context.Context, repo string, number int, update *IssueUpdate) (*Issue, error) {
	m.record("UpdateIssue", repo, number, update)
	return m.updateIssueResult, m.updateIssueErr
}

func (m *mockProvider) GetIssue(_ context.Context, repo string, number int) (*Issue, error) {
	m.record("GetIssue", repo, number)
	return m.getIssueResult, m.getIssueErr
}

func (m *mockProvider) ListIssues(_ context.Context, repo string, opts *ListOptions) ([]*Issue, error) {
	m.record("ListIssues", repo, opts)
	return m.listIssuesResult, m.listIssuesErr
}

func (m *mockProvider) AddComment(_ context.Context, repo string, number int, body string) (*Comment, error) {
	m.record("AddComment", repo, number, body)
	return m.addCommentResult, m.addCommentErr
}

func (m *mockProvider) ListPRs(_ context.Context, repo string, opts *ListOptions) ([]*PullRequest, error) {
	m.record("ListPRs", repo, opts)
	return m.listPRsResult, m.listPRsErr
}

func (m *mockProvider) GetPR(_ context.Context, repo string, number int) (*PullRequest, error) {
	m.record("GetPR", repo, number)
	return m.getPRResult, m.getPRErr
}

func (m *mockProvider) GetPRFiles(_ context.Context, repo string, number int) ([]*ChangedFile, error) {
	m.record("GetPRFiles", repo, number)
	return m.getPRFilesResult, m.getPRFilesErr
}

func (m *mockProvider) GetPRDiff(_ context.Context, repo string, number int) (string, error) {
	m.record("GetPRDiff", repo, number)
	return m.getPRDiffResult, m.getPRDiffErr
}

func (m *mockProvider) ListPRCommits(_ context.Context, repo string, number int) ([]*Commit, error) {
	m.record("ListPRCommits", repo, number)
	return m.listPRCommitsResult, m.listPRCommitsErr
}

func (m *mockProvider) ListPRReviews(_ context.Context, repo string, number int) ([]*Review, error) {
	m.record("ListPRReviews", repo, number)
	return m.listPRReviewsResult, m.listPRReviewsErr
}

func (m *mockProvider) SubmitReview(_ context.Context, repo string, number int, review *ReviewSubmission) (*Review, error) {
	m.record("SubmitReview", repo, number, review)
	return m.submitReviewResult, m.submitReviewErr
}

func (m *mockProvider) AddReviewComment(_ context.Context, repo string, number int, comment *ReviewComment) (*ReviewComment, error) {
	m.record("AddReviewComment", repo, number, comment)
	return m.addReviewCommentResult, m.addReviewCommentErr
}

func (m *mockProvider) ListChecks(_ context.Context, repo string, number int) ([]*CheckRun, error) {
	m.record("ListChecks", repo, number)
	return m.listChecksResult, m.listChecksErr
}

func (m *mockProvider) MergePR(_ context.Context, repo string, number int, opts *MergeOptions) (*MergeResult, error) {
	m.record("MergePR", repo, number, opts)
	return m.mergePRResult, m.mergePRErr
}

func (m *mockProvider) AddReaction(_ context.Context, repo string, number int, commentID int64, emoji string) error {
	m.record("AddReaction", repo, number, commentID, emoji)
	return m.addReactionErr
}

func (m *mockProvider) RequestReview(_ context.Context, repo string, number int, reviewers []string) error {
	m.record("RequestReview", repo, number, reviewers)
	return m.requestReviewErr
}

func (m *mockProvider) Search(_ context.Context, query string, kind SearchKind, limit int) ([]SearchResult, error) {
	m.record("Search", "", query, kind, limit)
	return m.searchResult, m.searchErr
}

// --- Mock temp file resolver ---

type mockTempFiles struct {
	files map[string]string // key: "convID:label" → value: path
}

func (m *mockTempFiles) Resolve(convID, label string) string {
	return m.files[convID+":"+label]
}

// --- Test helper ---

func newTestTools(provider ForgeProvider, owner string) *Tools {
	mgr := &Manager{
		providers: map[string]ForgeProvider{"test": provider},
		configs:   map[string]AccountConfig{"test": {Name: "test", Owner: owner}},
		order:     []string{"test"},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &Tools{
		manager: mgr,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func baseArgs(repo string) map[string]any {
	return map[string]any{"repo": repo}
}

// --- Argument helper tests ---

func TestArgHelpers(t *testing.T) {
	t.Run("stringArg", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			key  string
			want string
		}{
			{"present", map[string]any{"k": "hello"}, "k", "hello"},
			{"missing", map[string]any{}, "k", ""},
			{"wrong_type", map[string]any{"k": 42}, "k", ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := stringArg(tt.args, tt.key)
				if got != tt.want {
					t.Errorf("stringArg(%v, %q) = %q, want %q", tt.args, tt.key, got, tt.want)
				}
			})
		}
	})

	t.Run("intArg", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			key  string
			want int
		}{
			{"present", map[string]any{"k": float64(42)}, "k", 42},
			{"missing", map[string]any{}, "k", 0},
			{"wrong_type", map[string]any{"k": "nope"}, "k", 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := intArg(tt.args, tt.key)
				if got != tt.want {
					t.Errorf("intArg(%v, %q) = %d, want %d", tt.args, tt.key, got, tt.want)
				}
			})
		}
	})

	t.Run("int64Arg", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			key  string
			want int64
		}{
			{"present", map[string]any{"k": float64(999)}, "k", 999},
			{"missing", map[string]any{}, "k", 0},
			{"wrong_type", map[string]any{"k": true}, "k", 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := int64Arg(tt.args, tt.key)
				if got != tt.want {
					t.Errorf("int64Arg(%v, %q) = %d, want %d", tt.args, tt.key, got, tt.want)
				}
			})
		}
	})

	t.Run("stringSliceArg", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			key  string
			want []string
		}{
			{
				"from_any_slice",
				map[string]any{"k": []any{"a", "b", "c"}},
				"k",
				[]string{"a", "b", "c"},
			},
			{
				"from_string_slice",
				map[string]any{"k": []string{"x", "y"}},
				"k",
				[]string{"x", "y"},
			},
			{
				"missing",
				map[string]any{},
				"k",
				nil,
			},
			{
				"wrong_type",
				map[string]any{"k": "single"},
				"k",
				nil,
			},
			{
				"mixed_any_slice",
				map[string]any{"k": []any{"a", 42, "c"}},
				"k",
				[]string{"a", "c"},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := stringSliceArg(tt.args, tt.key)
				if !sliceEqual(got, tt.want) {
					t.Errorf("stringSliceArg(%v, %q) = %v, want %v", tt.args, tt.key, got, tt.want)
				}
			})
		}
	})
}

// --- resolveBody tests ---

func TestResolveBody(t *testing.T) {
	t.Run("plain_text_passthrough", func(t *testing.T) {
		tools := newTestTools(&mockProvider{name: "test"}, "owner")
		got, err := tools.resolveBody(context.Background(), "hello world")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hello world" {
			t.Errorf("resolveBody() = %q, want %q", got, "hello world")
		}
	})

	t.Run("temp_prefix_no_resolver", func(t *testing.T) {
		tools := newTestTools(&mockProvider{name: "test"}, "owner")
		// tempFiles is nil by default in newTestTools.
		_, err := tools.resolveBody(context.Background(), "temp:draft")
		if err == nil {
			t.Fatal("expected error for temp: with nil resolver")
		}
		if !strings.Contains(err.Error(), "not available") {
			t.Errorf("error = %q, want it to contain 'not available'", err.Error())
		}
	})

	t.Run("temp_prefix_no_convID_func", func(t *testing.T) {
		tools := newTestTools(&mockProvider{name: "test"}, "owner")
		tools.tempFiles = &mockTempFiles{}
		// conversationID is nil by default.
		_, err := tools.resolveBody(context.Background(), "temp:draft")
		if err == nil {
			t.Fatal("expected error for temp: with nil conversation ID func")
		}
		if !strings.Contains(err.Error(), "conversation ID") {
			t.Errorf("error = %q, want it to contain 'conversation ID'", err.Error())
		}
	})

	t.Run("temp_prefix_unknown_label", func(t *testing.T) {
		tools := newTestTools(&mockProvider{name: "test"}, "owner")
		tools.tempFiles = &mockTempFiles{files: map[string]string{}}
		tools.conversationID = func(_ context.Context) string { return "conv1" }

		_, err := tools.resolveBody(context.Background(), "temp:nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown temp label")
		}
		if !strings.Contains(err.Error(), "unknown temp label") {
			t.Errorf("error = %q, want it to contain 'unknown temp label'", err.Error())
		}
	})

	t.Run("temp_prefix_resolves_file", func(t *testing.T) {
		dir := t.TempDir()
		tmpFile := filepath.Join(dir, "draft.md")
		if err := os.WriteFile(tmpFile, []byte("resolved content"), 0o644); err != nil {
			t.Fatal(err)
		}

		tools := newTestTools(&mockProvider{name: "test"}, "owner")
		tools.tempFiles = &mockTempFiles{
			files: map[string]string{"conv1:draft": tmpFile},
		}
		tools.conversationID = func(_ context.Context) string { return "conv1" }

		got, err := tools.resolveBody(context.Background(), "temp:draft")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "resolved content" {
			t.Errorf("resolveBody() = %q, want %q", got, "resolved content")
		}
	})
}

// --- resolveAccountAndRepo tests ---

func TestResolveAccountAndRepo(t *testing.T) {
	tests := []struct {
		name     string
		owner    string
		args     map[string]any
		wantRepo string
		wantErr  string
	}{
		{
			name:     "bare_repo_gets_owner",
			owner:    "myorg",
			args:     map[string]any{"repo": "myrepo"},
			wantRepo: "myorg/myrepo",
		},
		{
			name:     "qualified_repo_passthrough",
			owner:    "myorg",
			args:     map[string]any{"repo": "other/repo"},
			wantRepo: "other/repo",
		},
		{
			name:    "missing_repo",
			owner:   "myorg",
			args:    map[string]any{},
			wantErr: "repo is required",
		},
		{
			name:    "bare_repo_no_owner",
			owner:   "",
			args:    map[string]any{"repo": "myrepo"},
			wantErr: "requires an owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp := &mockProvider{name: "test"}
			tools := newTestTools(mp, tt.owner)

			provider, repo, err := tools.resolveAccountAndRepo(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider == nil {
				t.Fatal("expected non-nil provider")
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

// --- HandleIssueGet tests ---

func TestHandleIssueGet(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			getIssueResult: &Issue{
				Number:    42,
				Title:     "Bug report",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"bug", "urgent"},
				Assignees: []string{"bob"},
				Body:      "Something is broken",
				URL:       "https://github.com/owner/repo/issues/42",
				CreatedAt: now,
				UpdatedAt: now,
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "myrepo",
			"number": float64(42),
		}
		got, err := tools.HandleIssueGet(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantParts := []string{
			"Issue #42: Bug report",
			"State: open | Author: alice",
			"Labels: bug, urgent",
			"Assignees: bob",
			"Created: 2025-01-15",
			"Something is broken",
		}
		for _, part := range wantParts {
			if !strings.Contains(got, part) {
				t.Errorf("output missing %q\ngot: %s", part, got)
			}
		}

		// Verify the provider was called with the resolved repo.
		if len(mp.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mp.calls))
		}
		if mp.calls[0].repo != "owner/myrepo" {
			t.Errorf("called with repo %q, want %q", mp.calls[0].repo, "owner/myrepo")
		}
	})

	t.Run("missing_number", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "myrepo"}
		_, err := tools.HandleIssueGet(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing number")
		}
		if !strings.Contains(err.Error(), "number is required") {
			t.Errorf("error = %q, want it to contain 'number is required'", err.Error())
		}
	})

	t.Run("missing_repo", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"number": float64(1)}
		_, err := tools.HandleIssueGet(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing repo")
		}
		if !strings.Contains(err.Error(), "repo is required") {
			t.Errorf("error = %q, want it to contain 'repo is required'", err.Error())
		}
	})

	t.Run("provider_error", func(t *testing.T) {
		mp := &mockProvider{
			name:        "test",
			getIssueErr: fmt.Errorf("API rate limited"),
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "myrepo", "number": float64(1)}
		_, err := tools.HandleIssueGet(context.Background(), args)
		if err == nil {
			t.Fatal("expected error from provider")
		}
		if !strings.Contains(err.Error(), "API rate limited") {
			t.Errorf("error = %q, want it to contain 'API rate limited'", err.Error())
		}
	})
}

// --- HandleIssueCreate tests ---

func TestHandleIssueCreate(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			createIssueResult: &Issue{
				Number: 99,
				Title:  "New feature",
				URL:    "https://github.com/org/repo/issues/99",
			},
		}
		tools := newTestTools(mp, "org")

		args := map[string]any{
			"repo":      "repo",
			"title":     "New feature",
			"body":      "Please add this",
			"labels":    []any{"enhancement"},
			"assignees": []any{"alice"},
		}
		got, err := tools.HandleIssueCreate(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "#99") {
			t.Errorf("output missing issue number: %s", got)
		}
		if !strings.Contains(got, "New feature") {
			t.Errorf("output missing title: %s", got)
		}

		// Verify provider received correct args.
		if len(mp.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mp.calls))
		}
		call := mp.calls[0]
		if call.method != "CreateIssue" {
			t.Errorf("method = %q, want CreateIssue", call.method)
		}
		if call.repo != "org/repo" {
			t.Errorf("repo = %q, want org/repo", call.repo)
		}
		issue := call.args[0].(*Issue)
		if issue.Title != "New feature" {
			t.Errorf("title = %q, want 'New feature'", issue.Title)
		}
		if issue.Body != "Please add this" {
			t.Errorf("body = %q, want 'Please add this'", issue.Body)
		}
		if !sliceEqual(issue.Labels, []string{"enhancement"}) {
			t.Errorf("labels = %v, want [enhancement]", issue.Labels)
		}
		if !sliceEqual(issue.Assignees, []string{"alice"}) {
			t.Errorf("assignees = %v, want [alice]", issue.Assignees)
		}
	})

	t.Run("missing_title", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "org")

		args := map[string]any{"repo": "repo"}
		_, err := tools.HandleIssueCreate(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing title")
		}
		if !strings.Contains(err.Error(), "title is required") {
			t.Errorf("error = %q, want it to contain 'title is required'", err.Error())
		}
	})
}

// --- HandleIssueList tests ---

func TestHandleIssueList(t *testing.T) {
	t.Run("empty_results", func(t *testing.T) {
		mp := &mockProvider{
			name:             "test",
			listIssuesResult: []*Issue{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "myrepo"}
		got, err := tools.HandleIssueList(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No issues found." {
			t.Errorf("got %q, want 'No issues found.'", got)
		}
	})

	t.Run("non_empty_results", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			listIssuesResult: []*Issue{
				{Number: 1, Title: "First", State: "open", Author: "alice", Labels: []string{"bug"}, Comments: 3},
				{Number: 2, Title: "Second", State: "closed", Author: "bob", Comments: 0},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":  "myrepo",
			"state": "all",
			"limit": float64(10),
		}
		got, err := tools.HandleIssueList(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantParts := []string{
			"Found 2 issue(s):",
			"#1 First (open) [bug]",
			"alice, 3 comments",
			"#2 Second (closed)",
			"bob, 0 comments",
		}
		for _, part := range wantParts {
			if !strings.Contains(got, part) {
				t.Errorf("output missing %q\ngot: %s", part, got)
			}
		}
	})
}

// --- HandlePRDiff truncation tests ---

func TestHandlePRDiff(t *testing.T) {
	t.Run("short_diff_no_truncation", func(t *testing.T) {
		diff := "line1\nline2\nline3"
		mp := &mockProvider{
			name:            "test",
			getPRDiffResult: diff,
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "myrepo",
			"number": float64(1),
		}
		got, err := tools.HandlePRDiff(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != diff {
			t.Errorf("got %q, want %q", got, diff)
		}
	})

	t.Run("truncation_at_max_lines", func(t *testing.T) {
		// Build a diff with 10 lines, truncate at 3.
		lines := make([]string, 10)
		for i := range lines {
			lines[i] = fmt.Sprintf("diff line %d", i)
		}
		diff := strings.Join(lines, "\n")

		mp := &mockProvider{
			name:            "test",
			getPRDiffResult: diff,
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":      "myrepo",
			"number":    float64(5),
			"max_lines": float64(3),
		}
		got, err := tools.HandlePRDiff(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(got, "diff line 0") {
			t.Error("output should contain the first line")
		}
		if !strings.Contains(got, "diff line 2") {
			t.Error("output should contain line at max_lines boundary")
		}
		if !strings.Contains(got, "[diff truncated, 7 more lines]") {
			t.Errorf("output missing truncation marker\ngot: %s", got)
		}
		if strings.Contains(got, "diff line 3") {
			t.Error("output should not contain lines beyond max_lines")
		}
	})

	t.Run("default_max_lines", func(t *testing.T) {
		// Build a diff with 2001 lines to check the default of 2000.
		lines := make([]string, 2001)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i)
		}
		diff := strings.Join(lines, "\n")

		mp := &mockProvider{
			name:            "test",
			getPRDiffResult: diff,
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "myrepo",
			"number": float64(1),
		}
		got, err := tools.HandlePRDiff(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "[diff truncated, 1 more lines]") {
			t.Errorf("expected truncation with default max_lines\ngot suffix: ...%s",
				got[max(0, len(got)-100):])
		}
	})
}

// --- HandlePRList tests ---

func TestHandlePRList(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mp := &mockProvider{
			name:          "test",
			listPRsResult: []*PullRequest{},
		}
		tools := newTestTools(mp, "owner")

		got, err := tools.HandlePRList(context.Background(), baseArgs("repo"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No pull requests found." {
			t.Errorf("got %q, want 'No pull requests found.'", got)
		}
	})

	t.Run("non_empty", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			listPRsResult: []*PullRequest{
				{Number: 10, Title: "Add feature", State: "open", Head: "feat", Base: "main", Author: "alice"},
			},
		}
		tools := newTestTools(mp, "owner")

		got, err := tools.HandlePRList(context.Background(), baseArgs("repo"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "#10 Add feature (open)") {
			t.Errorf("output missing PR info\ngot: %s", got)
		}
		if !strings.Contains(got, "feat → main") {
			t.Errorf("output missing branch info\ngot: %s", got)
		}
	})
}

// --- HandlePRGet tests ---

func TestHandlePRGet(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	mergeable := true

	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			getPRResult: &PullRequest{
				Number:       7,
				Title:        "Fix tests",
				State:        "open",
				Author:       "bob",
				Head:         "fix/tests",
				Base:         "main",
				Additions:    10,
				Deletions:    3,
				ChangedFiles: 2,
				Mergeable:    &mergeable,
				Body:         "This fixes the flaky tests.",
				URL:          "https://github.com/owner/repo/pull/7",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(7)}
		got, err := tools.HandlePRGet(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantParts := []string{
			"PR #7: Fix tests",
			"State: open | Author: bob",
			"Branch: fix/tests → main",
			"Changes: +10 -3 across 2 files",
			"Mergeable: true",
			"This fixes the flaky tests.",
		}
		for _, part := range wantParts {
			if !strings.Contains(got, part) {
				t.Errorf("output missing %q\ngot: %s", part, got)
			}
		}
	})
}

// --- HandleIssueComment tests ---

func TestHandleIssueComment(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			addCommentResult: &Comment{
				ID:  555,
				URL: "https://github.com/owner/repo/issues/1#comment-555",
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(1),
			"body":   "LGTM",
		}
		got, err := tools.HandleIssueComment(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "555") {
			t.Errorf("output missing comment ID\ngot: %s", got)
		}
	})

	t.Run("missing_body", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		_, err := tools.HandleIssueComment(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing body")
		}
		if !strings.Contains(err.Error(), "body is required") {
			t.Errorf("error = %q, want 'body is required'", err.Error())
		}
	})
}

// --- HandleReact tests ---

func TestHandleReact(t *testing.T) {
	t.Run("issue_reaction", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(5),
			"emoji":  "+1",
		}
		got, err := tools.HandleReact(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, ":+1:") {
			t.Errorf("output missing emoji\ngot: %s", got)
		}
		if !strings.Contains(got, "#5") {
			t.Errorf("output missing issue number\ngot: %s", got)
		}
	})

	t.Run("comment_reaction", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":       "repo",
			"number":     float64(5),
			"emoji":      "heart",
			"comment_id": float64(123),
		}
		got, err := tools.HandleReact(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "comment 123") {
			t.Errorf("output missing comment ID\ngot: %s", got)
		}
	})

	t.Run("missing_emoji", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(5)}
		_, err := tools.HandleReact(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing emoji")
		}
		if !strings.Contains(err.Error(), "emoji is required") {
			t.Errorf("error = %q, want 'emoji is required'", err.Error())
		}
	})
}

// --- HandleRequestReview tests ---

func TestHandleRequestReview(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":      "repo",
			"number":    float64(10),
			"reviewers": []any{"alice", "bob"},
		}
		got, err := tools.HandleRequestReview(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "alice, bob") {
			t.Errorf("output missing reviewers\ngot: %s", got)
		}
		if !strings.Contains(got, "PR #10") {
			t.Errorf("output missing PR number\ngot: %s", got)
		}
	})

	t.Run("missing_reviewers", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(10)}
		_, err := tools.HandleRequestReview(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing reviewers")
		}
		if !strings.Contains(err.Error(), "reviewers is required") {
			t.Errorf("error = %q, want 'reviewers is required'", err.Error())
		}
	})
}

// --- HandleSearch tests ---

func TestHandleSearch(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			searchResult: []SearchResult{
				{Number: 1, Title: "Found issue", URL: "https://example.com/1", Body: "snippet"},
				{Title: "Code result", URL: "https://example.com/code"},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"query": "search term",
			"kind":  "issues",
			"limit": float64(10),
		}
		got, err := tools.HandleSearch(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "Found 2 result(s)") {
			t.Errorf("output missing count\ngot: %s", got)
		}
		if !strings.Contains(got, "#1 Found issue") {
			t.Errorf("output missing issue result\ngot: %s", got)
		}
		// Code result has no number, so should not have "#0" prefix.
		if strings.Contains(got, "#0") {
			t.Errorf("output should not show #0 for numberless results\ngot: %s", got)
		}
	})

	t.Run("no_results", func(t *testing.T) {
		mp := &mockProvider{
			name:         "test",
			searchResult: []SearchResult{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"query": "nothing", "kind": "issues"}
		got, err := tools.HandleSearch(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No results found." {
			t.Errorf("got %q, want 'No results found.'", got)
		}
	})

	t.Run("missing_query", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"kind": "issues"}
		_, err := tools.HandleSearch(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing query")
		}
		if !strings.Contains(err.Error(), "query is required") {
			t.Errorf("error = %q, want 'query is required'", err.Error())
		}
	})

	t.Run("missing_kind", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"query": "test"}
		_, err := tools.HandleSearch(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing kind")
		}
		if !strings.Contains(err.Error(), "kind is required") {
			t.Errorf("error = %q, want 'kind is required'", err.Error())
		}
	})
}

// --- HandlePRMerge tests ---

func TestHandlePRMerge(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			mergePRResult: &MergeResult{
				SHA:     "abc123",
				Message: "Pull Request successfully merged",
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(15),
			"method": "squash",
		}
		got, err := tools.HandlePRMerge(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "abc123") {
			t.Errorf("output missing SHA\ngot: %s", got)
		}
		if !strings.Contains(got, "PR #15 merged") {
			t.Errorf("output missing merge confirmation\ngot: %s", got)
		}
	})
}

// --- HandlePRFiles tests ---

func TestHandlePRFiles(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mp := &mockProvider{
			name:             "test",
			getPRFilesResult: []*ChangedFile{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRFiles(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No changed files." {
			t.Errorf("got %q, want 'No changed files.'", got)
		}
	})

	t.Run("with_files", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			getPRFilesResult: []*ChangedFile{
				{Filename: "main.go", Status: "modified", Additions: 5, Deletions: 2, Patch: "@@ -1,3 +1,6 @@"},
				{Filename: "new.go", Status: "added", Additions: 10, Deletions: 0},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRFiles(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "2 changed file(s)") {
			t.Errorf("output missing file count\ngot: %s", got)
		}
		if !strings.Contains(got, "main.go (modified) +5 -2") {
			t.Errorf("output missing file info\ngot: %s", got)
		}
	})
}

// --- HandlePRCommits tests ---

func TestHandlePRCommits(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mp := &mockProvider{
			name:                "test",
			listPRCommitsResult: []*Commit{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRCommits(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No commits." {
			t.Errorf("got %q, want 'No commits.'", got)
		}
	})

	t.Run("multiline_message_truncates", func(t *testing.T) {
		now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		mp := &mockProvider{
			name: "test",
			listPRCommitsResult: []*Commit{
				{SHA: "abc1234", Message: "First line\nSecond line\nThird", Author: "alice", Date: now},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRCommits(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "First line") {
			t.Errorf("output missing first line\ngot: %s", got)
		}
		if strings.Contains(got, "Second line") {
			t.Errorf("output should not contain subsequent lines\ngot: %s", got)
		}
	})
}

// --- HandlePRReviews tests ---

func TestHandlePRReviews(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mp := &mockProvider{
			name:                "test",
			listPRReviewsResult: []*Review{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRReviews(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No reviews." {
			t.Errorf("got %q, want 'No reviews.'", got)
		}
	})

	t.Run("with_inline_comments", func(t *testing.T) {
		submitted := time.Date(2025, 4, 1, 14, 30, 0, 0, time.UTC)
		mp := &mockProvider{
			name: "test",
			listPRReviewsResult: []*Review{
				{
					ID:          100,
					Author:      "alice",
					State:       "CHANGES_REQUESTED",
					Body:        "Needs fixes",
					SubmittedAt: submitted,
					InlineComments: []*ReviewComment{
						{Path: "main.go", Line: 42, Body: "Typo here"},
					},
				},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRReviews(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "alice") {
			t.Errorf("output missing author\ngot: %s", got)
		}
		if !strings.Contains(got, "CHANGES_REQUESTED") {
			t.Errorf("output missing state\ngot: %s", got)
		}
		if !strings.Contains(got, "main.go:42: Typo here") {
			t.Errorf("output missing inline comment\ngot: %s", got)
		}
	})
}

// --- HandlePRChecks tests ---

func TestHandlePRChecks(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mp := &mockProvider{
			name:             "test",
			listChecksResult: []*CheckRun{},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRChecks(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "No check runs found." {
			t.Errorf("got %q, want 'No check runs found.'", got)
		}
	})

	t.Run("with_checks", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			listChecksResult: []*CheckRun{
				{Name: "ci/test", Status: "completed", Conclusion: "success"},
				{Name: "ci/lint", Status: "completed", Conclusion: "failure", DetailsURL: "https://ci.example.com/123"},
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(1)}
		got, err := tools.HandlePRChecks(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "ci/test: completed (success)") {
			t.Errorf("output missing check run\ngot: %s", got)
		}
		if !strings.Contains(got, "ci/lint: completed (failure)") {
			t.Errorf("output missing failed check\ngot: %s", got)
		}
		if !strings.Contains(got, "https://ci.example.com/123") {
			t.Errorf("output missing details URL\ngot: %s", got)
		}
	})
}

// --- HandleIssueUpdate tests ---

func TestHandleIssueUpdate(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			updateIssueResult: &Issue{
				Number: 5,
				Title:  "Updated title",
				URL:    "https://github.com/owner/repo/issues/5",
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(5),
			"title":  "Updated title",
			"state":  "closed",
		}
		got, err := tools.HandleIssueUpdate(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "Updated issue #5") {
			t.Errorf("output missing update confirmation\ngot: %s", got)
		}
	})

	t.Run("missing_number", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo"}
		_, err := tools.HandleIssueUpdate(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing number")
		}
		if !strings.Contains(err.Error(), "number is required") {
			t.Errorf("error = %q, want 'number is required'", err.Error())
		}
	})
}

// --- HandlePRReview tests ---

func TestHandlePRReview(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			submitReviewResult: &Review{
				ID:    200,
				State: "APPROVED",
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(3),
			"event":  "APPROVE",
			"body":   "Looks good!",
		}
		got, err := tools.HandlePRReview(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "200") {
			t.Errorf("output missing review ID\ngot: %s", got)
		}
		if !strings.Contains(got, "APPROVED") {
			t.Errorf("output missing state\ngot: %s", got)
		}
	})

	t.Run("missing_event", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(3), "body": "text"}
		_, err := tools.HandlePRReview(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing event")
		}
		if !strings.Contains(err.Error(), "event is required") {
			t.Errorf("error = %q, want 'event is required'", err.Error())
		}
	})

	t.Run("missing_body", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{"repo": "repo", "number": float64(3), "event": "APPROVE"}
		_, err := tools.HandlePRReview(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing body")
		}
		if !strings.Contains(err.Error(), "body is required") {
			t.Errorf("error = %q, want 'body is required'", err.Error())
		}
	})
}

// --- HandlePRReviewComment tests ---

func TestHandlePRReviewComment(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		mp := &mockProvider{
			name: "test",
			addReviewCommentResult: &ReviewComment{
				ID:   300,
				Path: "main.go",
				Line: 10,
			},
		}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(1),
			"body":   "Fix this",
			"path":   "main.go",
			"line":   float64(10),
		}
		got, err := tools.HandlePRReviewComment(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "300") {
			t.Errorf("output missing comment ID\ngot: %s", got)
		}
		if !strings.Contains(got, "main.go:10") {
			t.Errorf("output missing file:line\ngot: %s", got)
		}
	})

	t.Run("missing_path", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(1),
			"body":   "Fix this",
			"line":   float64(10),
		}
		_, err := tools.HandlePRReviewComment(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing path")
		}
		if !strings.Contains(err.Error(), "path is required") {
			t.Errorf("error = %q, want 'path is required'", err.Error())
		}
	})

	t.Run("missing_line", func(t *testing.T) {
		mp := &mockProvider{name: "test"}
		tools := newTestTools(mp, "owner")

		args := map[string]any{
			"repo":   "repo",
			"number": float64(1),
			"body":   "Fix this",
			"path":   "main.go",
		}
		_, err := tools.HandlePRReviewComment(context.Background(), args)
		if err == nil {
			t.Fatal("expected error for missing line")
		}
		if !strings.Contains(err.Error(), "line is required") {
			t.Errorf("error = %q, want 'line is required'", err.Error())
		}
	})
}

// --- sliceEqual helper ---

func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		// Treat nil and empty as equivalent for test comparisons.
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
