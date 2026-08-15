package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestPRGetOnIssueNumberReturnsTheIssue covers a real production
// confusion. Issues and pull requests share one number space, so
// asking for a PR by an issue's number is an ordinary mistake — and
// the forge reports it as a plain not-found, which the classifier then
// renders as "absent or outside this token's visibility". That is
// technically true and sends the reader toward a permissions theory,
// when the number exists and the answer was one call away. #1385 was
// diagnosed as a read-only-token problem for exactly this reason.
func TestPRGetOnIssueNumberReturnsTheIssue(t *testing.T) {
	t.Parallel()

	provider := &mockProvider{
		name:     "test",
		getPRErr: errors.New(`get PR #1385: not found under forge account "github-readonly"`),
		getIssueResult: &Issue{
			Number: 1385,
			Title:  "Telemetry pass-failure root cause",
			State:  "open",
			Author: "nugget",
			URL:    "https://github.com/owner/repo/issues/1385",
			Body:   "the issue body",
		},
	}
	tools := newTestTools(provider, "owner")

	raw, err := tools.HandlePRGet(context.Background(), map[string]any{
		"repo":   "repo",
		"number": float64(1385),
	})
	if err != nil {
		t.Fatalf("HandlePRGet() = %v, want the issue returned instead of a not-found", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["kind"] != "issue" {
		t.Errorf("kind = %v, want the result labelled an issue so it is not recorded as a PR", resp["kind"])
	}
	note, _ := resp["note"].(string)
	for _, want := range []string{"is an issue, not a pull request", "forge_issue_get"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q\nmissing %q", note, want)
		}
	}
	if resp["title"] != "Telemetry pass-failure root cause" {
		t.Errorf("title = %v, want the issue's title", resp["title"])
	}
}

// TestPRGetKeepsTheErrorWhenTheNumberIsNothing confirms the fallback
// does not swallow a genuine miss. When the number is neither a PR nor
// an issue, the original refusal — including its account attribution —
// is what the caller needs.
func TestPRGetKeepsTheErrorWhenTheNumberIsNothing(t *testing.T) {
	t.Parallel()

	provider := &mockProvider{
		name:        "test",
		getPRErr:    errors.New(`get PR #99: not found under forge account "github-readonly"`),
		getIssueErr: errors.New("get issue #99: not found"),
	}
	tools := newTestTools(provider, "owner")

	_, err := tools.HandlePRGet(context.Background(), map[string]any{
		"repo":   "repo",
		"number": float64(99),
	})
	if err == nil {
		t.Fatal("HandlePRGet() succeeded for a number that is neither PR nor issue")
	}
	if !strings.Contains(err.Error(), "github-readonly") {
		t.Errorf("error = %q, want the original account-attributed refusal preserved", err)
	}
}

// TestCommitSearchRejectsQualifierOnlyQueries stops a 422 the caller
// cannot predict. GitHub accepts qualifier-only queries for issues and
// code and refuses them for commits; production sent
// q=repo:nugget/thane-ai-agent and got a validation failure back from
// the far side, which reads like a broken tool rather than a missing
// argument.
func TestCommitSearchRejectsQualifierOnlyQueries(t *testing.T) {
	t.Parallel()

	tools := newTestTools(&mockProvider{name: "test"}, "owner")

	_, err := tools.HandleSearch(context.Background(), map[string]any{
		"query": "repo:nugget/thane-ai-agent",
		"kind":  "commits",
	})
	if err == nil {
		t.Fatal("HandleSearch() forwarded a qualifier-only commit search")
	}
	for _, want := range []string{"needs search text", "forge_pr_commits"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q\nmissing %q", err.Error(), want)
		}
	}
}

// TestQualifierGuardIsScopedToCommits keeps the guard off the searches
// GitHub genuinely accepts qualifier-only.
func TestQualifierGuardIsScopedToCommits(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"issues", "code"} {
		if qualifiersOnly("repo:nugget/thane") && SearchKind(kind) == SearchCommits {
			t.Fatalf("guard would fire for %q", kind)
		}
	}
	if !qualifiersOnly("repo:a/b is:open") {
		t.Error("qualifiersOnly() = false for an all-qualifier query")
	}
	if qualifiersOnly("repo:a/b timeout") {
		t.Error("qualifiersOnly() = true for a query carrying search text")
	}
}
