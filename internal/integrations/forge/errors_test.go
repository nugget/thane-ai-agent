package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v69/github"
)

// ghResponse builds an http.Response complete enough for go-github's
// error types to render themselves — their Error() methods dereference
// the originating request, so a bare status code panics.
func ghResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/nugget/thane"},
		},
	}
}

func TestClassifyGitHubError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want DenialKind
	}{
		{
			name: "nil error is unclassified",
			err:  nil,
			want: DenialNone,
		},
		{
			name: "plain error carries no forge signal",
			err:  errors.New("connection reset"),
			want: DenialNone,
		},
		{
			name: "401 marks an unusable token",
			err:  &github.ErrorResponse{Response: ghResponse(http.StatusUnauthorized), Message: "Bad credentials"},
			want: DenialUnauthenticated,
		},
		{
			name: "403 marks the token's access policy",
			err:  &github.ErrorResponse{Response: ghResponse(http.StatusForbidden), Message: "Resource not accessible by personal access token"},
			want: DenialForbidden,
		},
		{
			name: "404 may be absence or invisibility",
			err:  &github.ErrorResponse{Response: ghResponse(http.StatusNotFound), Message: "Not Found"},
			want: DenialInvisible,
		},
		{
			name: "500 is an upstream fault, not an authorization wall",
			err:  &github.ErrorResponse{Response: ghResponse(http.StatusInternalServerError), Message: "Server Error"},
			want: DenialNone,
		},
		{
			// The API reports rate limits with 403, so this case proves
			// the limit check runs before the status mapping: telling a
			// model "stop, this is permanent" when it should wait is the
			// exact confusion the classification exists to prevent.
			name: "primary rate limit outranks its 403 status",
			err:  &github.RateLimitError{Response: ghResponse(http.StatusForbidden), Message: "API rate limit exceeded"},
			want: DenialRateLimited,
		},
		{
			name: "secondary abuse limit is transient too",
			err:  &github.AbuseRateLimitError{Response: ghResponse(http.StatusForbidden), Message: "secondary rate limit"},
			want: DenialRateLimited,
		},
		{
			name: "classification survives wrapping by the provider",
			err:  fmt.Errorf("submit review on PR #12: %w", &github.ErrorResponse{Response: ghResponse(http.StatusForbidden), Message: "denied"}),
			want: DenialForbidden,
		},
		{
			name: "an ErrorResponse without a response is unclassified",
			err:  &github.ErrorResponse{Message: "malformed"},
			want: DenialNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyGitHubError(tt.err); got != tt.want {
				t.Errorf("classifyGitHubError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind DenialKind
		// wantSubstrings are the load-bearing pieces of the teaching
		// error: a model that reads the message must be able to tell
		// which account failed and whether retrying can ever work.
		wantSubstrings []string
		notSubstrings  []string
	}{
		{
			name:           "forbidden names the account and forecloses retry",
			kind:           DenialForbidden,
			wantSubstrings: []string{`"github-readonly"`, "access policy", "not a transient failure", "submit review on PR #12"},
		},
		{
			name:           "unauthenticated points at the credential",
			kind:           DenialUnauthenticated,
			wantSubstrings: []string{`"github-readonly"`, "expired, revoked, or malformed", "do not retry"},
		},
		{
			name:           "invisible explains the masked read",
			kind:           DenialInvisible,
			wantSubstrings: []string{`"github-readonly"`, "unauthorized reads with not-found", "outside this token's visibility"},
		},
		{
			name:           "rate limited invites a later retry",
			kind:           DenialRateLimited,
			wantSubstrings: []string{`"github-readonly"`, "transient", "wait"},
			notSubstrings:  []string{"access policy"},
		},
		{
			// Unclassified failures keep the shape they have always had,
			// so this change adds signal without rewriting every existing
			// forge error the model has learned to read.
			name:           "unclassified keeps the plain op: err shape",
			kind:           DenialNone,
			wantSubstrings: []string{"submit review on PR #12: boom"},
			notSubstrings:  []string{"github-readonly", "retry"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &ProviderError{
				Account: "github-readonly",
				Op:      "submit review on PR #12",
				Kind:    tt.kind,
				Err:     errors.New("boom"),
			}
			got := err.Error()
			for _, want := range tt.wantSubstrings {
				if !strings.Contains(got, want) {
					t.Errorf("Error() = %q\nmissing substring %q", got, want)
				}
			}
			for _, unwanted := range tt.notSubstrings {
				if strings.Contains(got, unwanted) {
					t.Errorf("Error() = %q\nunexpected substring %q", got, unwanted)
				}
			}
		})
	}
}

func TestProviderErrorUnnamedAccount(t *testing.T) {
	t.Parallel()

	err := &ProviderError{Op: "get repository", Kind: DenialForbidden, Err: errors.New("boom")}
	if got := err.Error(); !strings.Contains(got, "(default)") {
		t.Errorf("Error() = %q, want a placeholder for the unnamed account", got)
	}
}

func TestDeniedAndDenialKindOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantDenied bool
		wantKind   DenialKind
	}{
		{
			name:       "plain error is neither denied nor classified",
			err:        errors.New("boom"),
			wantDenied: false,
			wantKind:   DenialNone,
		},
		{
			name:       "forbidden is a permanent wall",
			err:        &ProviderError{Account: "ro", Kind: DenialForbidden, Err: errors.New("boom")},
			wantDenied: true,
			wantKind:   DenialForbidden,
		},
		{
			name:       "unauthenticated is a permanent wall",
			err:        &ProviderError{Account: "ro", Kind: DenialUnauthenticated, Err: errors.New("boom")},
			wantDenied: true,
			wantKind:   DenialUnauthenticated,
		},
		{
			// Transient by construction: a caller that escalated on a
			// rate limit would page a human for a wait.
			name:       "rate limited is not a denial",
			err:        &ProviderError{Account: "ro", Kind: DenialRateLimited, Err: errors.New("boom")},
			wantDenied: false,
			wantKind:   DenialRateLimited,
		},
		{
			// Absence and invisibility are indistinguishable here, so
			// this must not read as a confirmed authorization wall.
			name:       "invisibility is not a confirmed denial",
			err:        &ProviderError{Account: "ro", Kind: DenialInvisible, Err: errors.New("boom")},
			wantDenied: false,
			wantKind:   DenialInvisible,
		},
		{
			name:       "classification survives caller wrapping",
			err:        fmt.Errorf("handling forge_pr_review: %w", &ProviderError{Account: "ro", Kind: DenialForbidden, Err: errors.New("boom")}),
			wantDenied: true,
			wantKind:   DenialForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Denied(tt.err); got != tt.wantDenied {
				t.Errorf("Denied() = %v, want %v", got, tt.wantDenied)
			}
			if got := DenialKindOf(tt.err); got != tt.wantKind {
				t.Errorf("DenialKindOf() = %q, want %q", got, tt.wantKind)
			}
		})
	}
}

func TestProviderErrorUnwrapsToUnderlying(t *testing.T) {
	t.Parallel()

	underlying := &github.ErrorResponse{Response: ghResponse(http.StatusForbidden), Message: "denied"}
	err := &ProviderError{Account: "ro", Op: "merge PR #3", Kind: DenialForbidden, Err: underlying}

	var respErr *github.ErrorResponse
	if !errors.As(err, &respErr) {
		t.Fatal("errors.As() could not recover the go-github error through ProviderError")
	}
	if respErr.Message != "denied" {
		t.Errorf("recovered message = %q, want %q", respErr.Message, "denied")
	}
}
