package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// shrinkTestClient records the one call a shrink makes and answers it.
type shrinkTestClient struct {
	reply           llm.ChatResponse
	model           string
	messages        []llm.Message
	tools           []map[string]any
	maxOutputTokens int
}

func (c *shrinkTestClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	c.model, c.messages, c.tools, c.maxOutputTokens = model, messages, tools, llm.MaxOutputTokensFromContext(ctx)
	reply := c.reply
	return &reply, nil
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
// tool-free request naming the limit, with the projection text as the user
// turn, and a reply cut off at the output bound refused rather than saved.
func TestShrinkFacetTextAsksOnceWithNoTools(t *testing.T) {
	t.Parallel()

	client := &shrinkTestClient{reply: llm.ChatResponse{Message: llm.Message{Content: "Shorter."}, StopReason: "stop"}}
	got, err := shrinkFacetText(context.Background(), client, "edge/fast-model", "A long status line.", 120)
	if err != nil || got != "Shorter." {
		t.Fatalf("shrinkFacetText() = %q, %v; want the reply text", got, err)
	}
	if client.model != "edge/fast-model" || client.tools != nil || client.maxOutputTokens != 240 {
		t.Errorf("call = model %q, tools %v, max output %d; want edge/fast-model, no tools, 240", client.model, client.tools, client.maxOutputTokens)
	}
	if len(client.messages) != 2 || client.messages[0].Role != "system" || !strings.Contains(client.messages[0].Content, "at most 120 characters") ||
		client.messages[1].Role != "user" || client.messages[1].Content != "A long status line." {
		t.Errorf("messages = %+v, want the limit in the system turn and the text as the user turn", client.messages)
	}

	for _, reason := range []string{"length", "max_tokens"} {
		client.reply.StopReason = reason
		if got, err := shrinkFacetText(context.Background(), client, "edge/fast-model", "A long status line.", 120); err == nil {
			t.Errorf("stop reason %q: shrinkFacetText() = %q, want the cut-off reply refused", reason, got)
		}
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
		Shrink: func(context.Context, string, int) (string, error) { return saved, nil },
	}
	app := &App{documentStore: store, documentTools: documents.NewTools(store, shrinker)}
	hydrated, err := app.hydrateLoopOutputs(facetedSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	sent := strings.Repeat("s", 130)
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
	if got := decoded.Published.Shortened; len(got) != 1 || got[0].SavedText != saved || got[0].SentCharacters != 130 || got[0].SavedCharacters != len(saved) {
		t.Fatalf("result = %s, want status_line reported shortened from 130 to the saved text", result)
	}
	body, err := os.ReadFile(filepath.Join(coreDir, "office.md"))
	if err != nil {
		t.Fatalf("read published document: %v", err)
	}
	if !strings.Contains(string(body), saved) || strings.Contains(string(body), sent) {
		t.Fatalf("published document does not carry the shortened status line:\n%s", body)
	}
}
