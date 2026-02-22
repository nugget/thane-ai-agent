package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gogithub "github.com/google/go-github/v69/github"
)

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"acme/myapp", "acme", "myapp", false},
		{"noslash", "", "", true},
		{"/noleft", "", "", true},
		{"noright/", "", "", true},
		{"", "", "", true},
	}

	for _, tc := range tests {
		owner, name, err := splitRepo(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("splitRepo(%q) err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			continue
		}
		if owner != tc.wantOwner || name != tc.wantName {
			t.Errorf("splitRepo(%q) = (%q, %q), want (%q, %q)", tc.input, owner, name, tc.wantOwner, tc.wantName)
		}
	}
}

// newTestGitHubProvider creates a githubProvider pointed at a test server.
func newTestGitHubProvider(mux *http.ServeMux) (*githubProvider, *httptest.Server) {
	srv := httptest.NewServer(mux)
	client := gogithub.NewClient(nil).WithAuthToken("test-token")
	client, _ = client.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/")
	return &githubProvider{client: client, owner: "testowner"}, srv
}

func TestCreateIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/testowner/testrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		var req gogithub.IssueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"number":42,"title":%q,"body":%q,"state":"open","html_url":"https://github.com/testowner/testrepo/issues/42","user":{"login":"bot"}}`,
			*req.Title, *req.Body)
	})

	p, srv := newTestGitHubProvider(mux)
	defer srv.Close()

	issue, err := p.CreateIssue(t.Context(), "testowner/testrepo", &Issue{
		Title: "Test issue",
		Body:  "Test body",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42", issue.Number)
	}
	if issue.Title != "Test issue" {
		t.Errorf("issue.Title = %q, want %q", issue.Title, "Test issue")
	}
}

func TestRateLimitWarning(t *testing.T) {
	// Just ensure checkRateLimit doesn't panic with nil or low values.
	checkRateLimit(nil)

	resp := &gogithub.Response{
		Rate: gogithub.Rate{Remaining: 50},
	}
	checkRateLimit(resp) // should log a warning but not panic
}
