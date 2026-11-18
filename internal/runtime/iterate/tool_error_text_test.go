package iterate

import (
	"context"
	"errors"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// TestEngineShowsModelFacingToolErrors checks that a failed tool call's
// result shows the error's model-facing text when the tool wrote one,
// and its own text otherwise, while the tool-call record keeps the
// error's own text either way.
func TestEngineShowsModelFacingToolErrors(t *testing.T) {
	raw := errors.New("vision: read image /SENTINEL/path: API error 404: SENTINEL-BODY")
	tests := []struct {
		name       string
		err        error
		wantResult string
	}{
		{
			name:       "carrier",
			err:        &promptfmt.ModelFacingError{Text: "The image was not analyzed.", Err: raw},
			wantResult: "Error: The image was not analyzed.",
		},
		{
			name:       "plain error",
			err:        raw,
			wantResult: "Error: " + raw.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLLM{
				responses: []*llm.ChatResponse{
					toolCallResponse(makeToolCall("search", nil)),
					textResponse("done"),
				},
			}
			exec := &mockExecutor{
				results: map[string]string{},
				errors:  map[string]error{"search": tt.err},
			}
			cfg := baseCfg(mock, exec)
			var recorded string
			cfg.OnToolCallDone = func(_ context.Context, _, _, errMsg string) { recorded = errMsg }

			if _, err := (&Engine{}).Run(context.Background(), cfg, baseMessages()); err != nil {
				t.Fatal(err)
			}
			if len(mock.calls) < 2 {
				t.Fatal("expected a second model call carrying the tool result")
			}
			msgs := mock.calls[1].Messages
			if got := msgs[len(msgs)-1].Content; got != tt.wantResult {
				t.Errorf("tool result = %q, want %q", got, tt.wantResult)
			}
			if recorded != raw.Error() {
				t.Errorf("tool-call record = %q, want the error's own text %q", recorded, raw.Error())
			}
		})
	}
}
