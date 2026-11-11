package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// shrinkTestClient records the one call a shrink makes and answers it.
type shrinkTestClient struct {
	reply           llm.ChatResponse
	err             error
	blocks          bool
	model           string
	messages        []llm.Message
	tools           []map[string]any
	maxOutputTokens int
}

func (c *shrinkTestClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	c.model, c.messages, c.tools, c.maxOutputTokens = model, messages, tools, llm.MaxOutputTokensFromContext(ctx)
	if c.blocks {
		<-ctx.Done()
		return nil, fmt.Errorf("request failed: %w", ctx.Err())
	}
	reply := c.reply
	return &reply, c.err
}

func (c *shrinkTestClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.Chat(ctx, model, messages, tools)
}

func (c *shrinkTestClient) Ping(context.Context) error { return nil }

// TestFacetShrinkerIsOffUntilAModelIsConfigured pins the off switch: an
// empty models.facet_shrink_model builds no shrinker, so every faceted
// writer refuses an overrun exactly as it did before the key existed.
func TestFacetShrinkerIsOffUntilAModelIsConfigured(t *testing.T) {
	t.Parallel()

	a := &App{cfg: &config.Config{}, llmClient: &shrinkTestClient{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if shrinker := a.facetShrinker(); shrinker != nil {
		t.Fatalf("facetShrinker() = %+v with no model configured, want nil", shrinker)
	}
	a.cfg.Models.FacetShrinkModel = "edge/fast-model"
	shrinker := a.facetShrinker()
	if shrinker == nil || shrinker.Model != "edge/fast-model" || shrinker.Shrink == nil || shrinker.Logger == nil {
		t.Fatalf("facetShrinker() = %+v, want one calling edge/fast-model", shrinker)
	}
}

// TestShrinkFacetTextAsksOnceWithNoTools pins the call shape: one bounded,
// tool-free request naming the limit and asking to keep the text's line
// structure (a teaser may run to several lines; a single-line projection
// only reaches a shrink already single-line), with the projection text as
// the user turn.
// A reply cut off at the output bound is refused rather than saved, whether
// the provider reports the stop reason or only the tokens it spent, and its
// text comes back with the refusal for the shrinker's rejected-reply log.
func TestShrinkFacetTextAsksOnceWithNoTools(t *testing.T) {
	t.Parallel()

	statusLine, _ := documentfacets.FieldByKey("status_line")
	client := &shrinkTestClient{reply: llm.ChatResponse{Message: llm.Message{Content: "Shorter."}, StopReason: "stop"}}
	got, err := shrinkFacetText(context.Background(), client, "edge/fast-model", "A long status line.", statusLine, "20 words and 180 characters")
	if err != nil || got != "Shorter." {
		t.Fatalf("shrinkFacetText() = %q, %v; want the reply text", got, err)
	}
	if client.model != "edge/fast-model" || client.tools != nil || client.maxOutputTokens != 360 {
		t.Errorf("call = model %q, tools %v, max output %d; want edge/fast-model, no tools, 360", client.model, client.tools, client.maxOutputTokens)
	}
	if len(client.messages) != 2 || client.messages[0].Role != "system" || !strings.Contains(client.messages[0].Content, "at most 20 words and 180 characters.") ||
		client.messages[1].Role != "user" || client.messages[1].Content != "A long status line." {
		t.Errorf("messages = %+v, want the limit in the system turn and the text as the user turn", client.messages)
	}
	if system := client.messages[0].Content; !strings.Contains(system, "keep the text's line structure") || strings.Contains(system, "line breaks") {
		t.Errorf("system turn = %q, want it to keep the line structure rather than forbid line breaks", system)
	}

	for _, cutOff := range []llm.ChatResponse{
		{Message: llm.Message{Content: "Short"}, StopReason: "length"},
		{Message: llm.Message{Content: "Short"}, StopReason: "max_tokens"},
		// Ollama reports no stop reason; its token count still shows the
		// reply spent the whole bound (thinking can, leaving a fragment).
		{Message: llm.Message{Content: "Short"}, OutputTokens: 360},
	} {
		client.reply = cutOff
		if got, err := shrinkFacetText(context.Background(), client, "edge/fast-model", "A long status line.", statusLine, "20 words"); err == nil || got != "Short" {
			t.Errorf("cut-off reply %+v: shrinkFacetText() = %q, %v; want it refused with its text", cutOff, got, err)
		}
	}
	client.reply = llm.ChatResponse{Message: llm.Message{Content: "Shorter."}, OutputTokens: 359}
	if got, err := shrinkFacetText(context.Background(), client, "edge/fast-model", "A long status line.", statusLine, "20 words"); err != nil || got != "Shorter." {
		t.Errorf("reply one token under the bound: shrinkFacetText() = %q, %v; want it kept", got, err)
	}
}

// TestShrinkFacetTextNamesAModelThatReasonedInsteadOfAnswering pins the one
// misconfiguration the outcome cannot show: a shrink model with thinking on
// spends the output bound reasoning, so every shrink fails and the writer's
// refusal stands exactly as when the text could not be shortened. Whether
// the provider classifies the reply as reasoning-only, reports an empty
// answer at the bound (as an answer, or as an empty-completion error beside
// the response that says so), or, on a model too slow to reach the bound, the
// call runs out its deadline, the refusal names the fix; a call that failed
// for another reason, including the turn being cancelled, or a long answer
// cut off, does not.
func TestShrinkFacetTextNamesAModelThatReasonedInsteadOfAnswering(t *testing.T) {
	t.Parallel()

	reasoningOnly := llm.WithErrorClass(llm.ErrReasoningOnly, errors.New("openai_compat produced only reasoning"))
	emptyCompletion := llm.WithErrorClass(llm.ErrEmptyCompletion, errors.New("openai_compat returned an empty assistant completion"))
	tests := []struct {
		name      string
		reply     llm.ChatResponse
		err       error
		blocks    bool
		ctx       func() (context.Context, context.CancelFunc)
		wantFix   bool
		wantClass error
	}{
		{name: "provider classified it reasoning-only", err: reasoningOnly, wantFix: true, wantClass: llm.ErrReasoningOnly},
		{name: "empty answer stopped by length", reply: llm.ChatResponse{StopReason: "length"}, wantFix: true},
		{name: "blank answer stopped by max_tokens", reply: llm.ChatResponse{Message: llm.Message{Content: " \n"}, StopReason: "max_tokens"}, wantFix: true},
		{name: "empty answer spent the bound with no stop reason", reply: llm.ChatResponse{OutputTokens: 360}, wantFix: true},
		{name: "empty completion error at the bound by stop reason", reply: llm.ChatResponse{StopReason: "length", OutputTokens: 31}, err: emptyCompletion, wantFix: true, wantClass: llm.ErrEmptyCompletion},
		{name: "empty completion error at the bound by token count", reply: llm.ChatResponse{OutputTokens: 360}, err: emptyCompletion, wantFix: true, wantClass: llm.ErrEmptyCompletion},
		{name: "empty completion error short of the bound", reply: llm.ChatResponse{StopReason: "stop", OutputTokens: 3}, err: emptyCompletion, wantClass: llm.ErrEmptyCompletion},
		{name: "long answer cut off", reply: llm.ChatResponse{Message: llm.Message{Content: "Short"}, StopReason: "length"}},
		{name: "call failed", err: errors.New("connection refused")},
		{
			name: "no answer before the call's deadline", blocks: true, wantFix: true, wantClass: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
		},
		{
			name: "turn cancelled mid-call", blocks: true, wantClass: context.Canceled,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(10*time.Millisecond, cancel)
				return ctx, cancel
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tt.ctx != nil {
				ctx, cancel = tt.ctx()
			}
			defer cancel()
			client := &shrinkTestClient{reply: tt.reply, err: tt.err, blocks: tt.blocks}
			statusLine, _ := documentfacets.FieldByKey("status_line")
			_, err := shrinkFacetText(ctx, client, "edge/fast-model", "A long status line.", statusLine, "20 words")
			if err == nil {
				t.Fatal("shrinkFacetText() error = nil, want a refusal")
			}
			if got := strings.Contains(err.Error(), thinkingOffFix); got != tt.wantFix {
				t.Errorf("error = %q; names the thinking fix = %v, want %v", err, got, tt.wantFix)
			}
			if tt.wantClass != nil && !errors.Is(err, tt.wantClass) {
				t.Errorf("error = %v, want it to keep its %v class", err, tt.wantClass)
			}
		})
	}
}

// TestPublishToolSavesASmallOverrunShortened pins the loop-output writer's
// wiring: a publish_output call refused only for a status line a little
// over budget lands shortened, and the tool result the loop reads carries
// the saved text.
func TestPublishToolSavesASmallOverrunShortened(t *testing.T) {
	t.Parallel()

	const saved = "Printer online; two packages wait at the desk."
	store, coreDir := newLoopOutputDocumentStore(t)
	shrinker := &documentfacets.Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shrink: func(context.Context, string, documentfacets.Field, string) (string, error) { return saved, nil },
	}
	app := &App{documentStore: store, documentTools: documents.NewTools(store, shrinker)}
	hydrated, err := app.hydrateLoopOutputs(facetedSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	sent := strings.TrimSpace(strings.Repeat("word ", 22))
	result, err := publish.Handler(context.Background(), map[string]any{"status_line": sent, "teaser": "Fine.", "full": "Fine."})
	if err != nil {
		t.Fatalf("publish handler error = %v, want the shortened publish to land", err)
	}
	var decoded struct {
		Published struct {
			Shortened []documentfacets.Shortened `json:"shortened_projections"`
		} `json:"published"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}
	if got := decoded.Published.Shortened; len(got) != 1 || got[0].SavedText != saved || got[0].Sent != "22 words" || got[0].Saved != "8 words" {
		t.Fatalf("result = %s, want status_line reported shortened from 22 words to the saved text", result)
	}
	body, err := os.ReadFile(filepath.Join(coreDir, "office.md"))
	if err != nil {
		t.Fatalf("read published document: %v", err)
	}
	if !strings.Contains(string(body), saved) || strings.Contains(string(body), sent) {
		t.Fatalf("published document does not carry the shortened status line:\n%s", body)
	}
}
