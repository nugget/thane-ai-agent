package attachments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// sentinelProviderError is a provider failure whose body and a path in
// it must never reach a model.
func sentinelProviderError(status int) error {
	return fmt.Errorf(`API error %d: {"error":"SENTINEL-BODY at /SENTINEL/path"}`, status)
}

// visionCase sets up one way an image analysis can end.
type visionCase struct {
	name       string
	client     *mockLLMClient
	prepare    func(t *testing.T, store *Store, rec *Record)
	wantClass  error  // errors.Is target on the analysis error
	wantReason string // VisionFailureReason
	wantNext   string // next-move sentence in attachment_describe's text
	wantCalls  int    // vision model calls one analysis makes
}

func visionFailureCases() []visionCase {
	return []visionCase{
		{
			name:       "model does not exist",
			client:     &mockLLMClient{err: sentinelProviderError(404)},
			wantClass:  ErrVisionCall,
			wantReason: "the vision model call failed (the provider reported that the requested model does not exist)",
			wantNext:   " Calling again with the same model will fail the same way; the model argument can name a different image-capable model.",
			wantCalls:  1,
		},
		{
			name:       "provider error",
			client:     &mockLLMClient{err: sentinelProviderError(503)},
			wantClass:  ErrVisionCall,
			wantReason: "the vision model call failed (the provider failed while handling the request)",
			wantNext:   " Calling again may succeed.",
			wantCalls:  1,
		},
		{
			name:       "timeout",
			client:     &mockLLMClient{err: context.DeadlineExceeded},
			wantClass:  ErrVisionCall,
			wantReason: "the vision model call failed (the provider did not answer in time)",
			wantNext:   " Calling again may succeed.",
			wantCalls:  1,
		},
		{
			name:       "empty description",
			client:     &mockLLMClient{response: "   "},
			wantClass:  ErrNoDescription,
			wantReason: "the vision model returned an empty description",
			wantNext:   " Calling again may produce a description.",
			wantCalls:  1,
		},
		{
			name:       "too large",
			client:     &mockLLMClient{response: "never asked"},
			prepare:    func(_ *testing.T, _ *Store, rec *Record) { rec.Size = maxVisionImageSize + 1 },
			wantClass:  ErrImageTooLarge,
			wantReason: "the image is over the 20 MiB limit for vision analysis",
			wantCalls:  0,
		},
		{
			name:   "stored file missing",
			client: &mockLLMClient{response: "never asked"},
			prepare: func(t *testing.T, store *Store, rec *Record) {
				if err := os.Remove(store.AbsPath(rec)); err != nil {
					t.Fatal(err)
				}
			},
			wantClass:  os.ErrNotExist,
			wantReason: "the stored image could not be read for analysis",
			wantCalls:  0,
		},
	}
}

// TestAnalyzeNeverReturnsEmptyWithoutError checks that every way an
// image analysis can end without a description is an error of a known
// class, that the class survives Reanalyze too, and that the reason a
// model reads carries none of the error's own text.
func TestAnalyzeNeverReturnsEmptyWithoutError(t *testing.T) {
	for _, tc := range visionFailureCases() {
		for _, reanalyze := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reanalyze=%v", tc.name, reanalyze), func(t *testing.T) {
				client := &mockLLMClient{response: tc.client.response, err: tc.client.err}
				analyzer, store := newTestAnalyzer(t, client)
				rec := ingestTestImage(t, store, []byte("image bytes "+tc.name))
				if tc.prepare != nil {
					tc.prepare(t, store, rec)
				}

				var desc string
				var err error
				if reanalyze {
					desc, err = analyzer.Reanalyze(context.Background(), rec, "")
				} else {
					desc, err = analyzer.Analyze(context.Background(), rec)
				}
				if err == nil || desc != "" {
					t.Fatalf("analysis = (%q, %v), want no description and an error", desc, err)
				}
				if !errors.Is(err, tc.wantClass) {
					t.Errorf("error %v is not %v", err, tc.wantClass)
				}
				if client.calls != tc.wantCalls {
					t.Errorf("vision model calls = %d, want %d", client.calls, tc.wantCalls)
				}
				reason := VisionFailureReason(err)
				if reason != tc.wantReason {
					t.Errorf("VisionFailureReason = %q, want %q", reason, tc.wantReason)
				}
				if strings.Contains(reason, "SENTINEL") || strings.Contains(reason, store.AbsPath(rec)) {
					t.Errorf("reason leaks the error's own text: %q", reason)
				}
			})
		}
	}
}

// TestToolsDescribeTellsTheModelWhyThereIsNoDescription checks the text
// attachment_describe hands the model when analysis fails: why, that it
// has not seen the image, and whether calling again can help, with the
// error's own text kept for logs only.
func TestToolsDescribeTellsTheModelWhyThereIsNoDescription(t *testing.T) {
	for _, tc := range visionFailureCases() {
		for _, args := range []map[string]any{{}, {"model": "other-vision-model"}} {
			t.Run(fmt.Sprintf("%s/args=%v", tc.name, args), func(t *testing.T) {
				client := &mockLLMClient{response: tc.client.response, err: tc.client.err}
				analyzer, store := newTestAnalyzer(t, client)
				rec := ingestTestImage(t, store, []byte("image bytes "+tc.name))
				if tc.prepare != nil {
					tc.prepare(t, store, rec)
					// Describe loads the record by id, so a size the
					// test sets in memory has to reach the store.
					if rec.Size > maxVisionImageSize {
						if _, err := store.db.Exec(`UPDATE attachments SET size = ? WHERE id = ?`, rec.Size, rec.ID); err != nil {
							t.Fatal(err)
						}
					}
				}
				callArgs := map[string]any{"id": rec.ID}
				for k, v := range args {
					callArgs[k] = v
				}

				result, err := NewTools(store, analyzer).Describe(context.Background(), callArgs)
				if err == nil {
					t.Fatalf("Describe = %q with no error, want an error", result)
				}
				text, ok := promptfmt.ModelFacingText(err)
				if !ok {
					t.Fatalf("Describe error %v carries no model-facing text", err)
				}
				want := fmt.Sprintf("Attachment %s was not analyzed: %s. You do not know what this image shows.%s", rec.ID, tc.wantReason, tc.wantNext)
				if text != want {
					t.Errorf("model-facing text:\n got %q\nwant %q", text, want)
				}
				if strings.Contains(text, "SENTINEL") || strings.Contains(text, store.AbsPath(rec)) {
					t.Errorf("model-facing text leaks the error's own text: %q", text)
				}
				if !errors.Is(err, tc.wantClass) {
					t.Errorf("error %v lost its class %v", err, tc.wantClass)
				}
				if tc.client.err != nil && !strings.Contains(err.Error(), tc.client.err.Error()) {
					t.Errorf("operator error text %q lost the provider's own error %q", err.Error(), tc.client.err.Error())
				}
			})
		}
	}
}

func TestToolsDescribeWithoutAnalyzer(t *testing.T) {
	store := newTestStore(t)
	rec := ingestTestImage(t, store, []byte("never analyzed"))

	result, err := NewTools(store, nil).Describe(context.Background(), map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Attachment %s has no description, and vision analysis is not enabled here, so it cannot be analyzed. You do not know what this image shows.", rec.ID)
	if result != want {
		t.Errorf("Describe =\n %q\nwant\n %q", result, want)
	}
}

// TestVisionFailureReasonClasses pins the reason for each class on its
// own, outside an analysis, including an unclassified error.
func TestVisionFailureReasonClasses(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", ErrImageTooLarge), "the image is over the 20 MiB limit for vision analysis"},
		{fmt.Errorf("x: %w", ErrNoDescription), "the vision model returned an empty description"},
		{llm.WithErrorClass(ErrVisionCall, sentinelProviderError(429)), "the vision model call failed (the provider was rejecting requests as over its rate limit)"},
		{llm.WithErrorClass(ErrVisionCall, errors.New("weird")), "the vision model call failed (the request failed)"},
		{errors.New("read /SENTINEL/path: permission denied"), "the stored image could not be read for analysis"},
	}
	for _, tt := range tests {
		if got := VisionFailureReason(tt.err); got != tt.want {
			t.Errorf("VisionFailureReason(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
