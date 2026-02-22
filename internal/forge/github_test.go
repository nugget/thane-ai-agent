package forge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestGitHub creates a GitHub provider backed by the given handler.
// The test server is closed automatically when the test finishes.
func newTestGitHub(t *testing.T, handler http.Handler) *GitHub {
	t.Helper()

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	gh, err := NewGitHub(ts.Client(), "test-token", ts.URL, logger)
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return gh
}

func TestGitHubGetIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/repo/issues/42", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"number":     42,
			"title":      "Test issue",
			"body":       "Issue body text",
			"state":      "open",
			"html_url":   "https://github.com/owner/repo/issues/42",
			"comments":   3,
			"created_at": "2025-01-15T10:00:00Z",
			"updated_at": "2025-01-16T12:00:00Z",
			"user":       map[string]any{"login": "alice"},
			"labels":     []map[string]any{{"name": "bug"}, {"name": "urgent"}},
			"assignees":  []map[string]any{{"login": "bob"}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	issue, err := gh.GetIssue(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	if issue.Number != 42 {
		t.Errorf("Number = %d, want 42", issue.Number)
	}
	if issue.Title != "Test issue" {
		t.Errorf("Title = %q, want %q", issue.Title, "Test issue")
	}
	if issue.Body != "Issue body text" {
		t.Errorf("Body = %q, want %q", issue.Body, "Issue body text")
	}
	if issue.State != "open" {
		t.Errorf("State = %q, want %q", issue.State, "open")
	}
	if issue.Author != "alice" {
		t.Errorf("Author = %q, want %q", issue.Author, "alice")
	}
	if issue.CommentCount != 3 {
		t.Errorf("CommentCount = %d, want 3", issue.CommentCount)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "urgent" {
		t.Errorf("Labels = %v, want [bug urgent]", issue.Labels)
	}
	if len(issue.Assignees) != 1 || issue.Assignees[0] != "bob" {
		t.Errorf("Assignees = %v, want [bob]", issue.Assignees)
	}
}

func TestGitHubCreateIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}

		if req["title"] != "New issue" {
			t.Errorf("request title = %q, want %q", req["title"], "New issue")
		}
		if req["body"] != "Issue description" {
			t.Errorf("request body = %q, want %q", req["body"], "Issue description")
		}
		labels, ok := req["labels"].([]any)
		if !ok || len(labels) != 1 || labels[0] != "enhancement" {
			t.Errorf("request labels = %v, want [enhancement]", req["labels"])
		}

		resp := map[string]any{
			"number":     99,
			"title":      "New issue",
			"body":       "Issue description",
			"state":      "open",
			"html_url":   "https://github.com/owner/repo/issues/99",
			"created_at": "2025-01-20T08:00:00Z",
			"updated_at": "2025-01-20T08:00:00Z",
			"user":       map[string]any{"login": "alice"},
			"labels":     []map[string]any{{"name": "enhancement"}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	issue, err := gh.CreateIssue(context.Background(), "owner/repo", &Issue{
		Title:  "New issue",
		Body:   "Issue description",
		Labels: []string{"enhancement"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if issue.Number != 99 {
		t.Errorf("Number = %d, want 99", issue.Number)
	}
	if issue.Title != "New issue" {
		t.Errorf("Title = %q, want %q", issue.Title, "New issue")
	}
}

func TestGitHubListIssues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "open" {
			t.Errorf("state param = %q, want %q", q.Get("state"), "open")
		}
		if q.Get("labels") != "bug" {
			t.Errorf("labels param = %q, want %q", q.Get("labels"), "bug")
		}
		if q.Get("per_page") != "10" {
			t.Errorf("per_page param = %q, want %q", q.Get("per_page"), "10")
		}

		resp := []map[string]any{
			{
				"number":     1,
				"title":      "First",
				"state":      "open",
				"html_url":   "https://github.com/owner/repo/issues/1",
				"created_at": "2025-01-01T00:00:00Z",
				"updated_at": "2025-01-01T00:00:00Z",
				"user":       map[string]any{"login": "alice"},
			},
			{
				"number":     2,
				"title":      "Second",
				"state":      "open",
				"html_url":   "https://github.com/owner/repo/issues/2",
				"created_at": "2025-01-02T00:00:00Z",
				"updated_at": "2025-01-02T00:00:00Z",
				"user":       map[string]any{"login": "bob"},
			},
			// This entry is a PR (has pull_request links) and should be filtered out.
			{
				"number":       3,
				"title":        "A PR",
				"state":        "open",
				"html_url":     "https://github.com/owner/repo/pull/3",
				"created_at":   "2025-01-03T00:00:00Z",
				"updated_at":   "2025-01-03T00:00:00Z",
				"user":         map[string]any{"login": "carol"},
				"pull_request": map[string]any{"url": "https://api.github.com/repos/owner/repo/pulls/3"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	issues, err := gh.ListIssues(context.Background(), "owner/repo", &ListOptions{
		State:  "open",
		Labels: "bug",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	// The PR entry should be filtered out, leaving 2 real issues.
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2 (PR should be filtered)", len(issues))
	}
	if issues[0].Title != "First" {
		t.Errorf("issues[0].Title = %q, want %q", issues[0].Title, "First")
	}
	if issues[1].Title != "Second" {
		t.Errorf("issues[1].Title = %q, want %q", issues[1].Title, "Second")
	}
}

func TestGitHubGetPR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/repo/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"number":        7,
			"title":         "Add feature",
			"body":          "PR description",
			"state":         "open",
			"html_url":      "https://github.com/owner/repo/pull/7",
			"additions":     50,
			"deletions":     10,
			"changed_files": 3,
			"mergeable":     true,
			"created_at":    "2025-02-01T09:00:00Z",
			"updated_at":    "2025-02-02T14:00:00Z",
			"user":          map[string]any{"login": "dave"},
			"head":          map[string]any{"ref": "feat/new-thing", "sha": "abc123"},
			"base":          map[string]any{"ref": "main"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	pr, err := gh.GetPR(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}

	if pr.Number != 7 {
		t.Errorf("Number = %d, want 7", pr.Number)
	}
	if pr.Title != "Add feature" {
		t.Errorf("Title = %q, want %q", pr.Title, "Add feature")
	}
	if pr.Author != "dave" {
		t.Errorf("Author = %q, want %q", pr.Author, "dave")
	}
	if pr.Head != "feat/new-thing" {
		t.Errorf("Head = %q, want %q", pr.Head, "feat/new-thing")
	}
	if pr.Base != "main" {
		t.Errorf("Base = %q, want %q", pr.Base, "main")
	}
	if pr.Additions != 50 {
		t.Errorf("Additions = %d, want 50", pr.Additions)
	}
	if pr.Deletions != 10 {
		t.Errorf("Deletions = %d, want 10", pr.Deletions)
	}
	if pr.ChangedFiles != 3 {
		t.Errorf("ChangedFiles = %d, want 3", pr.ChangedFiles)
	}
	if pr.Mergeable == nil || !*pr.Mergeable {
		t.Error("Mergeable should be true")
	}
}

func TestGitHubGetPRDiff(t *testing.T) {
	const wantDiff = `diff --git a/file.go b/file.go
index abc..def 100644
--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 package main
+// added line
`

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/repo/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		// go-github sets Accept for diff format.
		if accept == "application/vnd.github.diff" || accept == "application/vnd.github.v3.diff" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(wantDiff))
			return
		}
		// Fallback: return JSON PR for any other Accept header (e.g., normal GetPR calls).
		resp := map[string]any{
			"number":     7,
			"title":      "Add feature",
			"state":      "open",
			"html_url":   "https://github.com/owner/repo/pull/7",
			"created_at": "2025-02-01T09:00:00Z",
			"updated_at": "2025-02-01T09:00:00Z",
			"user":       map[string]any{"login": "dave"},
			"head":       map[string]any{"ref": "feat/new-thing"},
			"base":       map[string]any{"ref": "main"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	diff, err := gh.GetPRDiff(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPRDiff: %v", err)
	}

	if diff != wantDiff {
		t.Errorf("diff mismatch:\ngot:  %q\nwant: %q", diff, wantDiff)
	}
}

func TestGitHubAuthHeader(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/repo/issues/1", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := map[string]any{
			"number":     1,
			"title":      "Auth test",
			"state":      "open",
			"html_url":   "https://github.com/owner/repo/issues/1",
			"created_at": "2025-01-01T00:00:00Z",
			"updated_at": "2025-01-01T00:00:00Z",
			"user":       map[string]any{"login": "alice"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	_, err := gh.GetIssue(context.Background(), "owner/repo", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestGitHubAddReaction_Issue(t *testing.T) {
	var calledPath string
	var reqBody map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/repos/owner/repo/issues/5/reactions", func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)

		resp := map[string]any{
			"id":      1,
			"content": "+1",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	err := gh.AddReaction(context.Background(), "owner/repo", 5, 0, "+1")
	if err != nil {
		t.Fatalf("AddReaction: %v", err)
	}

	if calledPath != "/api/v3/repos/owner/repo/issues/5/reactions" {
		t.Errorf("called path = %q, want issues reaction endpoint", calledPath)
	}
	if reqBody["content"] != "+1" {
		t.Errorf("reaction content = %q, want %q", reqBody["content"], "+1")
	}
}

func TestGitHubAddReaction_Comment(t *testing.T) {
	var calledPath string
	var reqBody map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/repos/owner/repo/issues/comments/999/reactions", func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)

		resp := map[string]any{
			"id":      2,
			"content": "heart",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	err := gh.AddReaction(context.Background(), "owner/repo", 5, 999, "heart")
	if err != nil {
		t.Fatalf("AddReaction: %v", err)
	}

	if calledPath != "/api/v3/repos/owner/repo/issues/comments/999/reactions" {
		t.Errorf("called path = %q, want comment reaction endpoint", calledPath)
	}
	if reqBody["content"] != "heart" {
		t.Errorf("reaction content = %q, want %q", reqBody["content"], "heart")
	}
}

func TestGitHubListChecks(t *testing.T) {
	mux := http.NewServeMux()

	// First call: GetPR to retrieve head SHA.
	mux.HandleFunc("GET /api/v3/repos/owner/repo/pulls/10", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"number":     10,
			"title":      "Check PR",
			"state":      "open",
			"html_url":   "https://github.com/owner/repo/pull/10",
			"created_at": "2025-03-01T00:00:00Z",
			"updated_at": "2025-03-01T00:00:00Z",
			"user":       map[string]any{"login": "eve"},
			"head":       map[string]any{"ref": "feat/checks", "sha": "deadbeef1234567890"},
			"base":       map[string]any{"ref": "main"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Second call: ListCheckRunsForRef.
	mux.HandleFunc("GET /api/v3/repos/owner/repo/commits/deadbeef1234567890/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		now := time.Date(2025, 3, 1, 1, 0, 0, 0, time.UTC)
		resp := map[string]any{
			"total_count": 2,
			"check_runs": []map[string]any{
				{
					"name":         "CI / test",
					"status":       "completed",
					"conclusion":   "success",
					"started_at":   now.Format(time.RFC3339),
					"completed_at": now.Add(5 * time.Minute).Format(time.RFC3339),
					"details_url":  "https://github.com/owner/repo/actions/runs/1",
				},
				{
					"name":        "CI / lint",
					"status":      "in_progress",
					"conclusion":  "",
					"started_at":  now.Format(time.RFC3339),
					"details_url": "https://github.com/owner/repo/actions/runs/2",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	checks, err := gh.ListChecks(context.Background(), "owner/repo", 10)
	if err != nil {
		t.Fatalf("ListChecks: %v", err)
	}

	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}

	if checks[0].Name != "CI / test" {
		t.Errorf("checks[0].Name = %q, want %q", checks[0].Name, "CI / test")
	}
	if checks[0].Status != "completed" {
		t.Errorf("checks[0].Status = %q, want %q", checks[0].Status, "completed")
	}
	if checks[0].Conclusion != "success" {
		t.Errorf("checks[0].Conclusion = %q, want %q", checks[0].Conclusion, "success")
	}
	if checks[0].CompletedAt == nil {
		t.Error("checks[0].CompletedAt should not be nil")
	}

	if checks[1].Name != "CI / lint" {
		t.Errorf("checks[1].Name = %q, want %q", checks[1].Name, "CI / lint")
	}
	if checks[1].Status != "in_progress" {
		t.Errorf("checks[1].Status = %q, want %q", checks[1].Status, "in_progress")
	}
}

func TestGitHubSearchIssues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "repo:owner/repo bug" {
			t.Errorf("query param = %q, want %q", q.Get("q"), "repo:owner/repo bug")
		}
		if q.Get("per_page") != "5" {
			t.Errorf("per_page = %q, want %q", q.Get("per_page"), "5")
		}

		resp := map[string]any{
			"total_count": 1,
			"items": []map[string]any{
				{
					"number":   10,
					"title":    "Found bug",
					"html_url": "https://github.com/owner/repo/issues/10",
					"body":     "Bug description here",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	gh := newTestGitHub(t, mux)
	results, err := gh.Search(context.Background(), "repo:owner/repo bug", SearchIssues, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Number != 10 {
		t.Errorf("Number = %d, want 10", results[0].Number)
	}
	if results[0].Title != "Found bug" {
		t.Errorf("Title = %q, want %q", results[0].Title, "Found bug")
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"org/my-project", "org", "my-project", false},
		{"noslash", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, name, err := splitRepo(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitRepo(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}
