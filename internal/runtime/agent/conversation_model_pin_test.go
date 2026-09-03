package agent

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"gopkg.in/yaml.v3"
)

// pinTestConfig describes a fleet where the router, left alone, prefers
// the local text-only model and only the cloud model can see images.
func pinTestConfig() *config.Config {
	return &config.Config{
		Models: config.ModelsConfig{
			Default:    "qwen3:8b",
			LocalFirst: true,
			Resources: map[string]config.ModelServerConfig{
				"local": {URL: "http://localhost:11434", Provider: "ollama"},
				"cloud": {URL: "https://api.anthropic.com", Provider: "anthropic"},
			},
			Available: []config.ModelConfig{
				{Name: "qwen3:8b", Resource: "local", SupportsTools: true, ContextWindow: 32768, Speed: 10, Quality: 7, CostTier: 0},
				{Name: "claude-sonnet-4-20250514", Resource: "cloud", SupportsTools: true, ContextWindow: 200000, Speed: 4, Quality: 9, CostTier: 3},
			},
		},
	}
}

func newPinTestLoop(t *testing.T, mock *mockLLM) (*Loop, *fleet.Registry) {
	t.Helper()
	loop := buildTestLoop(mock, nil)
	registry := testModelRegistryFromConfig(t, pinTestConfig())
	loop.UseModelRegistry(registry)
	loop.router = router.NewRouter(slog.Default(), registry.Catalog().RouterConfig(32))
	return loop, registry
}

func okResponses(n int) []*llm.ChatResponse {
	out := make([]*llm.ChatResponse, n)
	for i := range out {
		out[i] = &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "ok"}}
	}
	return out
}

func runTextTurn(t *testing.T, loop *Loop, convID, model, text string) {
	t.Helper()
	if _, err := loop.Run(context.Background(), &Request{
		ConversationID: convID,
		Model:          model,
		Messages:       []Message{{Role: "user", Content: text}},
	}, nil); err != nil {
		t.Fatalf("Run(%q, model=%q): %v", convID, model, err)
	}
}

func systemPromptOf(t *testing.T, call mockLLMCall) string {
	t.Helper()
	if len(call.Messages) == 0 || call.Messages[0].Role != "system" {
		t.Fatalf("first LLM message is not a system prompt: %+v", call.Messages)
	}
	return call.Messages[0].Content
}

func TestPinConversationModel_ResolvesAndRefuses(t *testing.T) {
	loop, registry := newPinTestLoop(t, &mockLLM{})

	t.Run("resolves a model name to its deployment", func(t *testing.T) {
		pin, err := loop.PinConversationModel("signal-alice", "claude-sonnet-4-20250514", "compare minds")
		if err != nil {
			t.Fatalf("PinConversationModel: %v", err)
		}
		if pin.Model != "claude-sonnet-4-20250514" || pin.Provider != "anthropic" || pin.Resource != "cloud" {
			t.Fatalf("pin = %+v, want claude on anthropic/cloud", pin)
		}
		if !pin.SupportsTools || !pin.SupportsImages || pin.ContextWindow != 200000 {
			t.Fatalf("pin capabilities = %+v, want tools+images and the configured window", pin)
		}
		if pin.Reason != "compare minds" || pin.PinnedAt.IsZero() {
			t.Fatalf("pin metadata = %+v, want reason and pinned_at recorded", pin)
		}
		got, ok := loop.ConversationModelPin("signal-alice")
		if !ok || got.Model != pin.Model {
			t.Fatalf("ConversationModelPin = %+v, %v; want the pin just set", got, ok)
		}
	})

	t.Run("replacing a pin keeps one per conversation", func(t *testing.T) {
		if _, err := loop.PinConversationModel("signal-alice", "qwen3:8b", ""); err != nil {
			t.Fatalf("PinConversationModel: %v", err)
		}
		got, _ := loop.ConversationModelPin("signal-alice")
		if got.Model != "qwen3:8b" {
			t.Fatalf("pin after replace = %q, want qwen3:8b", got.Model)
		}
		prev, had := loop.ClearConversationModelPin("signal-alice")
		if !had || prev.Model != "qwen3:8b" {
			t.Fatalf("ClearConversationModelPin = %+v, %v; want the replaced pin", prev, had)
		}
		if _, ok := loop.ConversationModelPin("signal-alice"); ok {
			t.Fatal("pin survived ClearConversationModelPin")
		}
		if _, had := loop.ClearConversationModelPin("signal-alice"); had {
			t.Fatal("second clear reported a pin")
		}
	})

	refusals := []struct {
		name string
		conv string
		ref  string
		want string
	}{
		{name: "unknown model names the discovery tool", conv: "signal-alice", ref: "nope", want: "model_registry_list"},
		{name: "empty model", conv: "signal-alice", ref: "  ", want: "model is required"},
		{name: "empty conversation", conv: "", ref: "qwen3:8b", want: "conversation id is required"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loop.PinConversationModel(tc.conv, tc.ref, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	t.Run("inactive deployment is refused with the policy tool named", func(t *testing.T) {
		if err := registry.ApplyDeploymentPolicy("qwen3:8b", fleet.DeploymentPolicy{State: fleet.DeploymentPolicyStateInactive, Reason: "test"}, time.Now()); err != nil {
			t.Fatalf("ApplyDeploymentPolicy: %v", err)
		}
		t.Cleanup(func() {
			_ = registry.ApplyDeploymentPolicy("qwen3:8b", fleet.DeploymentPolicy{State: fleet.DeploymentPolicyStateActive, Reason: "test"}, time.Now())
		})
		_, err := loop.PinConversationModel("signal-alice", "qwen3:8b", "")
		if err == nil || !strings.Contains(err.Error(), "inactive") || !strings.Contains(err.Error(), "model_deployment_set_policy") {
			t.Fatalf("error = %v, want inactive refusal naming model_deployment_set_policy", err)
		}
	})
}

func TestPinConversationModel_RefusesToollessAndAmbiguous(t *testing.T) {
	loop := buildTestLoop(&mockLLM{}, nil)
	// YAML rather than a struct literal: an explicit supports_tools: false
	// is a configured override, while a zero-valued struct field reads as
	// "unset" and inherits the provider's capability.
	var cfg config.Config
	raw := `
models:
  default: toolless
  resources:
    a:
      url: http://a.example:11434
      provider: ollama
    b:
      url: http://b.example:11434
      provider: ollama
  available:
    - name: toolless
      resource: a
      supports_tools: false
      context_window: 8192
    - name: twin
      resource: a
      supports_tools: true
      context_window: 8192
    - name: twin
      resource: b
      supports_tools: true
      context_window: 8192
`
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &cfg))

	if _, err := loop.PinConversationModel("owu-1", "toolless", ""); err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("toolless pin error = %v, want a tool-support refusal", err)
	}
	_, err := loop.PinConversationModel("owu-1", "twin", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous pin error = %v, want the candidates listed", err)
	}
	if _, ok := loop.ConversationModelPin("owu-1"); ok {
		t.Fatal("a refused pin was stored")
	}
}

func TestRun_ConversationPinOutranksRouterAndRequestModel(t *testing.T) {
	mock := &mockLLM{responses: okResponses(6)}
	loop, _ := newPinTestLoop(t, mock)
	const conv = "signal-alice"

	runTextTurn(t, loop, conv, "", "what is the status")
	if got := mock.calls[0].Model; got != "qwen3:8b" {
		t.Fatalf("unpinned turn model = %q, want the router's local pick qwen3:8b", got)
	}

	if _, err := loop.PinConversationModel(conv, "claude-sonnet-4-20250514", "user asked for sonnet"); err != nil {
		t.Fatalf("PinConversationModel: %v", err)
	}

	runTextTurn(t, loop, conv, "", "and now?")
	if got := mock.calls[1].Model; got != "claude-sonnet-4-20250514" {
		t.Fatalf("pinned turn model = %q, want claude-sonnet-4-20250514", got)
	}
	if prompt := systemPromptOf(t, mock.calls[1]); !strings.Contains(prompt, "claude-sonnet-4-20250514 (pinned -") {
		t.Fatalf("pinned turn context line missing pin marker:\n%s", prompt)
	}

	runTextTurn(t, loop, conv, "qwen3:8b", "explicit request model")
	if got := mock.calls[2].Model; got != "claude-sonnet-4-20250514" {
		t.Fatalf("pin vs request model: got %q, want the pin to win", got)
	}

	runTextTurn(t, loop, "owu-bob", "", "a different conversation")
	if got := mock.calls[3].Model; got != "qwen3:8b" {
		t.Fatalf("other conversation model = %q, want it unaffected by the pin", got)
	}

	if _, had := loop.ClearConversationModelPin(conv); !had {
		t.Fatal("ClearConversationModelPin found nothing to clear")
	}
	runTextTurn(t, loop, conv, "", "back to automatic")
	if got := mock.calls[4].Model; got != "qwen3:8b" {
		t.Fatalf("cleared turn model = %q, want the router back in charge", got)
	}
	if prompt := systemPromptOf(t, mock.calls[4]); strings.Contains(prompt, "pinned") {
		t.Fatalf("cleared turn context line still mentions a pin:\n%s", prompt)
	}
}

func TestRun_ConversationPinFallsBackWhenTurnNeedsImages(t *testing.T) {
	mock := &mockLLM{responses: okResponses(2)}
	loop, _ := newPinTestLoop(t, mock)
	const conv = "signal-alice"

	if _, err := loop.PinConversationModel(conv, "qwen3:8b", "local only please"); err != nil {
		t.Fatalf("PinConversationModel: %v", err)
	}

	image := llm.ImageContent{
		Data:      base64.StdEncoding.EncodeToString([]byte("not really a png")),
		MediaType: "image/png",
	}
	if _, err := loop.Run(context.Background(), &Request{
		ConversationID: conv,
		Messages:       []Message{{Role: "user", Content: "what is this", Images: []llm.ImageContent{image}}},
	}, nil); err != nil {
		t.Fatalf("Run with image: %v", err)
	}
	if got := mock.calls[0].Model; got != "claude-sonnet-4-20250514" {
		t.Fatalf("image turn model = %q, want the router's vision-capable pick", got)
	}
	prompt := systemPromptOf(t, mock.calls[0])
	if !strings.Contains(prompt, "pinned qwen3:8b skipped this turn: it does not support image inputs") {
		t.Fatalf("fallback turn context line does not explain the skipped pin:\n%s", prompt)
	}

	pin, ok := loop.ConversationModelPin(conv)
	if !ok {
		t.Fatal("pin was dropped by the fallback; it must survive")
	}
	if pin.LastFallback == nil || pin.LastFallback.Model != "claude-sonnet-4-20250514" || !strings.Contains(pin.LastFallback.Reason, "image") {
		t.Fatalf("pin.LastFallback = %+v, want the routed model and the image reason", pin.LastFallback)
	}

	runTextTurn(t, loop, conv, "", "thanks, text again")
	if got := mock.calls[1].Model; got != "qwen3:8b" {
		t.Fatalf("text turn after fallback model = %q, want the pin honored again", got)
	}
	if prompt := systemPromptOf(t, mock.calls[1]); !strings.Contains(prompt, "qwen3:8b (pinned -") {
		t.Fatalf("post-fallback context line missing pin marker:\n%s", prompt)
	}
}

func TestRun_ConversationPinWithoutRouterFailsLikeExplicitModel(t *testing.T) {
	mock := &mockLLM{responses: okResponses(1)}
	loop := buildTestLoop(mock, nil)
	loop.UseModelRegistry(testModelRegistryFromConfig(t, pinTestConfig()))
	const conv = "signal-alice"

	if _, err := loop.PinConversationModel(conv, "qwen3:8b", ""); err != nil {
		t.Fatalf("PinConversationModel: %v", err)
	}
	_, err := loop.Run(context.Background(), &Request{
		ConversationID: conv,
		Messages: []Message{{Role: "user", Content: "look", Images: []llm.ImageContent{{
			Data: base64.StdEncoding.EncodeToString([]byte("x")), MediaType: "image/png",
		}}}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("Run error = %v, want the explicit-model incompatibility with no router to fall back to", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0", len(mock.calls))
	}
}

func TestRun_ConversationPinRoutesAroundDeploymentDeactivatedAfterPinning(t *testing.T) {
	mock := &mockLLM{responses: okResponses(2)}
	loop, registry := newPinTestLoop(t, mock)
	const conv = "signal-alice"

	if _, err := loop.PinConversationModel(conv, "qwen3:8b", "local please"); err != nil {
		t.Fatalf("PinConversationModel: %v", err)
	}
	runTextTurn(t, loop, conv, "", "still local?")
	if got := mock.calls[0].Model; got != "qwen3:8b" {
		t.Fatalf("pinned turn model = %q, want qwen3:8b", got)
	}

	// Operator policy switches the pinned deployment off after the pin was
	// accepted. The router sees it through a config sync; the pin must see
	// it through preflight on its next turn.
	if err := registry.ApplyDeploymentPolicy("qwen3:8b", fleet.DeploymentPolicy{State: fleet.DeploymentPolicyStateInactive, Reason: "maintenance"}, time.Now()); err != nil {
		t.Fatalf("ApplyDeploymentPolicy: %v", err)
	}
	loop.router.UpdateConfig(registry.Catalog().RouterConfig(32))

	runTextTurn(t, loop, conv, "", "and now?")
	if got := mock.calls[1].Model; got != "claude-sonnet-4-20250514" {
		t.Fatalf("turn after deactivation model = %q, want the router's replacement", got)
	}
	if prompt := systemPromptOf(t, mock.calls[1]); !strings.Contains(prompt, "pinned qwen3:8b skipped this turn: this deployment is currently inactive by operator policy") {
		t.Fatalf("context line does not explain the deactivated pin:\n%s", prompt)
	}
	pin, ok := loop.ConversationModelPin(conv)
	if !ok || pin.LastFallback == nil || !strings.Contains(pin.LastFallback.Reason, "inactive") {
		t.Fatalf("pin after deactivation = %+v, %v; want it kept with the inactive fallback recorded", pin, ok)
	}
}

func TestConversationModelPins_FallbackOnlyLandsOnTheObservedPin(t *testing.T) {
	var pins conversationModelPins
	t0 := time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC)
	first := tools.ConversationModelPin{ConversationID: "signal-alice", Model: "qwen3:8b", PinnedAt: t0}
	pins.set(first)

	// A turn observed `first`; meanwhile the conversation was re-pinned.
	replacement := tools.ConversationModelPin{ConversationID: "signal-alice", Model: "claude-sonnet-4-20250514", PinnedAt: t0.Add(time.Minute)}
	pins.set(replacement)
	pins.recordFallback(first, "claude-sonnet-4-20250514", "it does not support image inputs", t0.Add(2*time.Minute))
	if got, _ := pins.get("signal-alice"); got.LastFallback != nil {
		t.Fatalf("replacement pin inherited a fallback from its predecessor: %+v", got.LastFallback)
	}

	// The same deployment re-pinned later is a different pin too.
	repinned := tools.ConversationModelPin{ConversationID: "signal-alice", Model: "qwen3:8b", PinnedAt: t0.Add(3 * time.Minute)}
	pins.set(repinned)
	pins.recordFallback(first, "claude-sonnet-4-20250514", "stale", t0.Add(4*time.Minute))
	if got, _ := pins.get("signal-alice"); got.LastFallback != nil {
		t.Fatalf("re-pinned deployment inherited a stale fallback: %+v", got.LastFallback)
	}

	// The live pin's own verdict lands.
	pins.recordFallback(repinned, "claude-sonnet-4-20250514", "it does not support image inputs", t0.Add(5*time.Minute))
	got, _ := pins.get("signal-alice")
	if got.LastFallback == nil || got.LastFallback.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("live pin fallback = %+v, want it recorded", got.LastFallback)
	}

	// A cleared pin records nothing.
	pins.clear("signal-alice")
	pins.recordFallback(repinned, "x", "y", t0.Add(6*time.Minute))
	if _, ok := pins.get("signal-alice"); ok {
		t.Fatal("recordFallback resurrected a cleared pin")
	}
}
