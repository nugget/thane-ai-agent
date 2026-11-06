package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// wireRequest is what a provider client put on the wire for one chat
// call: everything that depends on how the client was configured, and
// nothing that varies per call (request IDs, timing).
type wireRequest struct {
	Path          string
	Authorization string
	Body          []byte
}

// wireRecorder is a model server that answers every chat call with a
// minimal success in the dialect of the path it was called on, and
// records what it was sent.
type wireRecorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []wireRequest
}

func newWireRecorder(t *testing.T) *wireRecorder {
	t.Helper()
	rec := &wireRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			rec.record(r, body)
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		case r.URL.Path == "/api/chat":
			rec.record(r, body)
			_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *wireRecorder) record(r *http.Request, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = append(rec.reqs, wireRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body})
}

// chat sends one fixed call through client and returns what reached
// the wire.
func (rec *wireRecorder) chat(t *testing.T, client llm.Client) wireRequest {
	t.Helper()
	rec.mu.Lock()
	before := len(rec.reqs)
	rec.mu.Unlock()

	if _, err := client.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reqs) != before+1 {
		t.Fatalf("chat calls recorded = %d, want 1", len(rec.reqs)-before)
	}
	return rec.reqs[before]
}

// priorWiring builds a resource client exactly the way BuildClients did
// before NewResourceClient existed. It is the reference the refactor is
// held to: a change here is a change to production wiring, and the
// test comparing against it should fail until that change is meant.
func priorWiring(res Resource, cfg *config.Config, logger *slog.Logger) llm.Client {
	var rc config.ModelServerConfig
	if cfg != nil {
		rc = cfg.Models.Resources[res.ID]
	}
	apply := func(c *modelproviders.OpenAICompatClient) {
		if rc.StreamIdleTimeout != 0 {
			c.SetStreamIdleTimeout(rc.StreamIdleTimeout)
		}
		if len(rc.ChatTemplateKwargs) != 0 {
			c.SetChatTemplateKwargs(rc.ChatTemplateKwargs)
		}
	}
	switch res.Provider {
	case "ollama":
		return modelproviders.NewOllamaClient(res.URL, logger.With("resource", res.ID))
	case "lmstudio":
		lc := modelproviders.NewLMStudioClientWithTTL(res.URL, rc.APIKey, res.ID, logger.With("resource", res.ID), res.IdleTTLSeconds)
		apply(lc.OpenAICompatClient)
		return lc
	case "openai_compat":
		oc := modelproviders.NewOpenAICompatClient(res.URL, rc.APIKey, "openai_compat", res.ID, logger.With("resource", res.ID), 0)
		apply(oc)
		return oc
	}
	return nil
}

func resourcesConfig(id string, rc config.ModelServerConfig) *config.Config {
	return &config.Config{Models: config.ModelsConfig{Resources: map[string]config.ModelServerConfig{id: rc}}}
}

// decodeBody returns the top-level JSON fields of a request body.
func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode request body %q: %v", body, err)
	}
	return m
}

// assertOneHandle checks that exactly the typed handle for provider is
// set, and that it is the same client as rc.Client.
func assertOneHandle(t *testing.T, rc *ResourceClient, provider string) {
	t.Helper()
	handles := map[string]any{}
	if rc.Ollama != nil {
		handles["ollama"] = rc.Ollama
	}
	if rc.LMStudio != nil {
		handles["lmstudio"] = rc.LMStudio
	}
	if rc.OpenAICompat != nil {
		handles["openai_compat"] = rc.OpenAICompat
	}
	if rc.Anthropic != nil {
		handles["anthropic"] = rc.Anthropic
	}
	if len(handles) != 1 {
		t.Fatalf("typed handles set = %v, want only %q", reflect.ValueOf(handles).MapKeys(), provider)
	}
	h, ok := handles[provider]
	if !ok {
		t.Fatalf("typed handle for %q not set; got %v", provider, reflect.ValueOf(handles).MapKeys())
	}
	if h != any(rc.Client) {
		t.Fatalf("typed handle %T is not the same value as Client %T", h, rc.Client)
	}
}

// TestNewResourceClientWiresLikeBefore holds every endpoint provider to
// the wiring BuildClients applied before the constructor was factored
// out: the same bytes, path and credentials reach the server, and the
// facts that matter are pinned explicitly so a matching regression in
// both builders still fails.
func TestNewResourceClientWiresLikeBefore(t *testing.T) {
	t.Parallel()

	nested := map[string]any{"enable_thinking": false, "extra": map[string]any{"depth": float64(2)}}

	tests := []struct {
		name       string
		res        Resource
		rc         *config.ModelServerConfig // nil: no config entry for the resource
		nilCfg     bool
		wantPath   string
		wantAuth   string
		wantTTL    any // nil: ttl absent from the body
		wantKwargs any // nil: chat_template_kwargs absent from the body
	}{
		{
			name:     "ollama",
			res:      Resource{ID: "res-a", Provider: "ollama"},
			rc:       &config.ModelServerConfig{APIKey: "unused"},
			wantPath: "/api/chat",
		},
		{
			name:       "lmstudio with key, ttl and kwargs",
			res:        Resource{ID: "res-a", Provider: "lmstudio", IdleTTLSeconds: 600},
			rc:         &config.ModelServerConfig{APIKey: "test-key", ChatTemplateKwargs: map[string]any{"enable_thinking": false}},
			wantPath:   "/v1/chat/completions",
			wantAuth:   "Bearer test-key",
			wantTTL:    float64(600),
			wantKwargs: map[string]any{"enable_thinking": false},
		},
		{
			name:     "lmstudio without a config entry",
			res:      Resource{ID: "res-a", Provider: "lmstudio"},
			wantPath: "/v1/chat/completions",
		},
		{
			name:       "openai_compat with key and nested kwargs",
			res:        Resource{ID: "res-a", Provider: "openai_compat"},
			rc:         &config.ModelServerConfig{APIKey: "test-key", ChatTemplateKwargs: nested},
			wantPath:   "/v1/chat/completions",
			wantAuth:   "Bearer test-key",
			wantKwargs: nested,
		},
		{
			// The ttl field is an LM Studio extension; another server
			// must never be sent it, even if the catalog carries one.
			name:     "openai_compat never sends ttl",
			res:      Resource{ID: "res-a", Provider: "openai_compat", IdleTTLSeconds: 600},
			wantPath: "/v1/chat/completions",
		},
		{
			name:     "openai_compat with nil config",
			res:      Resource{ID: "res-a", Provider: "openai_compat"},
			nilCfg:   true,
			wantPath: "/v1/chat/completions",
		},
		{
			// Another resource's credentials and kwargs must not leak.
			name:     "config for a different resource is not applied",
			res:      Resource{ID: "res-a", Provider: "openai_compat"},
			wantPath: "/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newWireRecorder(t)
			res := tt.res
			res.URL = rec.srv.URL

			var cfg *config.Config
			switch {
			case tt.nilCfg:
			case tt.rc != nil:
				cfg = resourcesConfig(res.ID, *tt.rc)
			default:
				cfg = resourcesConfig("res-other", config.ModelServerConfig{
					APIKey:             "other-key",
					ChatTemplateKwargs: map[string]any{"enable_thinking": true},
				})
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			rc, err := NewResourceClient(res, cfg, WithClientLogger(logger))
			if err != nil {
				t.Fatalf("NewResourceClient: %v", err)
			}
			assertOneHandle(t, rc, res.Provider)
			if rc.Health == nil || rc.Health.Ping == nil || rc.Health.AttachWatcher == nil {
				t.Fatalf("Health = %+v, want ping and watch for an endpoint provider", rc.Health)
			}
			if err := rc.Health.Ping(context.Background()); err != nil {
				t.Fatalf("Health.Ping: %v", err)
			}

			got := rec.chat(t, rc.Client)
			want := rec.chat(t, priorWiring(res, cfg, logger))
			if got.Path != want.Path || got.Authorization != want.Authorization || !bytes.Equal(got.Body, want.Body) {
				t.Fatalf("wire differs from prior wiring:\n got  %s %q %s\n want %s %q %s",
					got.Path, got.Authorization, got.Body, want.Path, want.Authorization, want.Body)
			}

			if got.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tt.wantPath)
			}
			if got.Authorization != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got.Authorization, tt.wantAuth)
			}
			body := decodeBody(t, got.Body)
			if v, ok := body["ttl"]; ok != (tt.wantTTL != nil) || (ok && v != tt.wantTTL) {
				t.Errorf("ttl = %v (present %v), want %v", v, ok, tt.wantTTL)
			}
			if v, ok := body["chat_template_kwargs"]; ok != (tt.wantKwargs != nil) || (ok && !reflect.DeepEqual(v, tt.wantKwargs)) {
				t.Errorf("chat_template_kwargs = %#v (present %v), want %#v", v, ok, tt.wantKwargs)
			}
		})
	}
}

// TestNewResourceClientLogger pins which logger each client's own
// lines go through: endpoint clients add the resource attribute, which
// is how an operator tells two endpoints of one provider apart, and the
// account-wide Anthropic client takes the logger as given.
func TestNewResourceClientLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider     string
		wantResource bool
	}{
		{provider: "ollama", wantResource: true},
		{provider: "lmstudio", wantResource: true},
		{provider: "openai_compat", wantResource: true},
		{provider: "anthropic", wantResource: false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			cfg := &config.Config{Anthropic: config.AnthropicConfig{APIKey: "test-key"}}

			rc, err := NewResourceClient(Resource{ID: "res-a", Provider: tt.provider, URL: "http://127.0.0.1:1"}, cfg, WithClientLogger(logger))
			if err != nil {
				t.Fatalf("NewResourceClient: %v", err)
			}
			var clientLogger *slog.Logger
			switch {
			case rc.Ollama != nil:
				clientLogger = rc.Ollama.Logger()
			case rc.LMStudio != nil:
				clientLogger = rc.LMStudio.Logger()
			case rc.OpenAICompat != nil:
				clientLogger = rc.OpenAICompat.Logger()
			case rc.Anthropic != nil:
				clientLogger = rc.Anthropic.Logger()
			}
			clientLogger.Info("probe")

			line := buf.String()
			if !strings.Contains(line, `"msg":"probe"`) {
				t.Fatalf("client logger does not write through the given logger; got %q", line)
			}
			if got := strings.Contains(line, `"resource":"res-a"`); got != tt.wantResource {
				t.Fatalf("resource attribute present = %v, want %v: %s", got, tt.wantResource, line)
			}
		})
	}
}

// TestNewResourceClientOptionsOverride pins that an option replaces the
// value the resource or config would supply, explicit zero and empty
// values included, which is what lets a replay reproduce a recorded
// call's serving settings instead of the local config's.
func TestNewResourceClientOptionsOverride(t *testing.T) {
	t.Parallel()

	cfgKwargs := map[string]any{"enable_thinking": false}
	tests := []struct {
		name       string
		provider   string
		opts       []ResourceClientOption
		wantTTL    any
		wantKwargs any
	}{
		{
			name:       "no options keeps resource and config values",
			provider:   "lmstudio",
			wantTTL:    float64(600),
			wantKwargs: cfgKwargs,
		},
		{
			name:       "ttl override",
			provider:   "lmstudio",
			opts:       []ResourceClientOption{WithIdleTTLSeconds(30)},
			wantTTL:    float64(30),
			wantKwargs: cfgKwargs,
		},
		{
			name:       "zero ttl override omits the field",
			provider:   "lmstudio",
			opts:       []ResourceClientOption{WithIdleTTLSeconds(0)},
			wantKwargs: cfgKwargs,
		},
		{
			name:       "kwargs override",
			provider:   "lmstudio",
			opts:       []ResourceClientOption{WithChatTemplateKwargs(map[string]any{"enable_thinking": true})},
			wantTTL:    float64(600),
			wantKwargs: map[string]any{"enable_thinking": true},
		},
		{
			name:     "empty kwargs override sends none",
			provider: "lmstudio",
			opts:     []ResourceClientOption{WithChatTemplateKwargs(nil)},
			wantTTL:  float64(600),
		},
		{
			name:       "ttl option is ignored by openai_compat",
			provider:   "openai_compat",
			opts:       []ResourceClientOption{WithIdleTTLSeconds(30)},
			wantKwargs: cfgKwargs,
		},
		{
			name:       "later option wins and nil options are skipped",
			provider:   "lmstudio",
			opts:       []ResourceClientOption{WithIdleTTLSeconds(30), nil, WithIdleTTLSeconds(45)},
			wantTTL:    float64(45),
			wantKwargs: cfgKwargs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newWireRecorder(t)
			res := Resource{ID: "res-a", Provider: tt.provider, URL: rec.srv.URL, IdleTTLSeconds: 600}
			cfg := resourcesConfig(res.ID, config.ModelServerConfig{ChatTemplateKwargs: cfgKwargs})

			rc, err := NewResourceClient(res, cfg, tt.opts...)
			if err != nil {
				t.Fatalf("NewResourceClient: %v", err)
			}
			body := decodeBody(t, rec.chat(t, rc.Client).Body)
			if v, ok := body["ttl"]; ok != (tt.wantTTL != nil) || (ok && v != tt.wantTTL) {
				t.Errorf("ttl = %v (present %v), want %v", v, ok, tt.wantTTL)
			}
			if v, ok := body["chat_template_kwargs"]; ok != (tt.wantKwargs != nil) || (ok && !reflect.DeepEqual(v, tt.wantKwargs)) {
				t.Errorf("chat_template_kwargs = %#v (present %v), want %#v", v, ok, tt.wantKwargs)
			}
		})
	}
}

// TestNewResourceClientStreamIdleTimeout pins which silence window a
// stalled stream is abandoned after: the configured one, or the
// option's in its place. A want of zero means the guard is disabled,
// so the stream must outlast several configured windows.
func TestNewResourceClientStreamIdleTimeout(t *testing.T) {
	t.Parallel()

	const configured = 100 * time.Millisecond
	const override = 140 * time.Millisecond
	disable := []ResourceClientOption{WithStreamIdleTimeout(0)}

	tests := []struct {
		name     string
		provider string
		opts     []ResourceClientOption
		want     time.Duration
	}{
		{name: "openai_compat uses the configured window", provider: "openai_compat", want: configured},
		{name: "lmstudio uses the configured window", provider: "lmstudio", want: configured},
		{name: "openai_compat option replaces it", provider: "openai_compat", opts: []ResourceClientOption{WithStreamIdleTimeout(override)}, want: override},
		{name: "lmstudio option replaces it", provider: "lmstudio", opts: []ResourceClientOption{WithStreamIdleTimeout(override)}, want: override},
		{name: "openai_compat zero option disables the guard", provider: "openai_compat", opts: disable},
		{name: "lmstudio zero option disables the guard", provider: "lmstudio", opts: disable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseStream := func() { releaseOnce.Do(func() { close(release) }) }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":[]}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			defer releaseStream()

			res := Resource{ID: "res-a", Provider: tt.provider, URL: srv.URL}
			cfg := resourcesConfig(res.ID, config.ModelServerConfig{StreamIdleTimeout: configured})
			rc, err := NewResourceClient(res, cfg, tt.opts...)
			if err != nil {
				t.Fatalf("NewResourceClient: %v", err)
			}

			done := make(chan error, 1)
			go func() {
				_, err := rc.Client.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, func(llm.StreamEvent) {})
				done <- err
			}()

			if tt.want == 0 {
				select {
				case err := <-done:
					t.Fatalf("stream with the guard disabled returned early: %v", err)
				case <-time.After(4 * configured):
				}
				releaseStream()
			}

			select {
			case err := <-done:
				var idle *modelproviders.StreamIdleTimeoutError
				isIdle := errors.As(err, &idle)
				if tt.want == 0 {
					if isIdle {
						t.Fatalf("stream with the guard disabled was abandoned after %v", idle.Idle)
					}
					return
				}
				if !isIdle {
					t.Fatalf("stalled stream error = %v, want a stream idle timeout", err)
				}
				if idle.Idle != tt.want {
					t.Fatalf("idle window = %v, want %v", idle.Idle, tt.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("stalled stream never returned")
			}
		})
	}
}

// TestNewResourceClientEffectiveStreamIdleTimeout pins the window each
// built client ends up with. A config entry that leaves
// stream_idle_timeout unset (zero) must keep the client default, or
// every resource without that line would have its guard disabled; only
// an explicit option may disable it, and it does so over a configured
// window. The window never reaches the wire and the default is too long
// to wait out, so it is read off the client rather than a stalled
// stream.
func TestNewResourceClientEffectiveStreamIdleTimeout(t *testing.T) {
	t.Parallel()

	const configured = 100 * time.Millisecond
	const override = 140 * time.Millisecond
	withWindow := config.ModelServerConfig{StreamIdleTimeout: configured}
	withoutWindow := config.ModelServerConfig{ChatTemplateKwargs: map[string]any{"enable_thinking": false}}

	tests := []struct {
		name string
		cfg  *config.Config
		opts []ResourceClientOption
		want time.Duration
	}{
		{name: "nil config keeps the default", want: modelproviders.DefaultStreamIdleTimeout},
		{name: "config for another resource keeps the default", cfg: resourcesConfig("res-other", withWindow), want: modelproviders.DefaultStreamIdleTimeout},
		{name: "entry without stream_idle_timeout keeps the default", cfg: resourcesConfig("res-a", withoutWindow), want: modelproviders.DefaultStreamIdleTimeout},
		{name: "configured window applies", cfg: resourcesConfig("res-a", withWindow), want: configured},
		{name: "option replaces the configured window", cfg: resourcesConfig("res-a", withWindow), opts: []ResourceClientOption{WithStreamIdleTimeout(override)}, want: override},
		{name: "zero option disables over a configured window", cfg: resourcesConfig("res-a", withWindow), opts: []ResourceClientOption{WithStreamIdleTimeout(0)}, want: 0},
		{name: "zero option disables over an unset window", cfg: resourcesConfig("res-a", withoutWindow), opts: []ResourceClientOption{WithStreamIdleTimeout(0)}, want: 0},
	}

	for _, provider := range []string{"openai_compat", "lmstudio"} {
		for _, tt := range tests {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				res := Resource{ID: "res-a", Provider: provider, URL: "http://model.invalid"}
				rc, err := NewResourceClient(res, tt.cfg, tt.opts...)
				if err != nil {
					t.Fatalf("NewResourceClient: %v", err)
				}
				var got time.Duration
				switch provider {
				case "openai_compat":
					got = rc.OpenAICompat.StreamIdleTimeout()
				case "lmstudio":
					got = rc.LMStudio.StreamIdleTimeout()
				}
				if got != tt.want {
					t.Errorf("stream idle timeout = %v, want %v", got, tt.want)
				}
			})
		}
	}
}

// TestNewResourceClientAnthropicAndRefusals covers the provider with no
// endpoint, and the resources NewResourceClient must refuse.
func TestNewResourceClientAnthropicAndRefusals(t *testing.T) {
	t.Parallel()

	configured := &config.Config{Anthropic: config.AnthropicConfig{APIKey: "test-key"}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	shared := modelproviders.NewAnthropicClient("test-key", quiet)

	tests := []struct {
		name       string
		res        Resource
		cfg        *config.Config
		opts       []ResourceClientOption
		wantErr    string
		wantShared bool
	}{
		{name: "anthropic builds its own client", res: Resource{ID: "cloud-a", Provider: "anthropic"}, cfg: configured},
		{
			name:       "anthropic reuses the shared client",
			res:        Resource{ID: "cloud-a", Provider: "anthropic"},
			cfg:        configured,
			opts:       []ResourceClientOption{WithSharedAnthropicClient(shared)},
			wantShared: true,
		},
		{
			name:    "anthropic without config is refused",
			res:     Resource{ID: "cloud-a", Provider: "anthropic"},
			cfg:     &config.Config{},
			wantErr: `resource "cloud-a" requires anthropic config`,
		},
		{
			// A shared client is no licence to skip the config check.
			name:    "anthropic without config is refused even with a shared client",
			res:     Resource{ID: "cloud-a", Provider: "anthropic"},
			cfg:     &config.Config{},
			opts:    []ResourceClientOption{WithSharedAnthropicClient(shared)},
			wantErr: `resource "cloud-a" requires anthropic config`,
		},
		{
			name:    "anthropic with nil config is refused",
			res:     Resource{ID: "cloud-a", Provider: "anthropic"},
			wantErr: `resource "cloud-a" requires anthropic config`,
		},
		{
			name:    "unknown provider is refused",
			res:     Resource{ID: "res-a", Provider: "carrier-pigeon"},
			cfg:     configured,
			wantErr: `provider "carrier-pigeon" is not implemented for resource "res-a"`,
		},
		{
			name:    "empty provider is refused",
			res:     Resource{ID: "res-a"},
			cfg:     configured,
			wantErr: `provider "" is not implemented for resource "res-a"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := append([]ResourceClientOption{WithClientLogger(quiet)}, tt.opts...)
			rc, err := NewResourceClient(tt.res, tt.cfg, opts...)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if rc != nil {
					t.Fatalf("refused resource still returned a client: %+v", rc)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewResourceClient: %v", err)
			}
			assertOneHandle(t, rc, "anthropic")
			if rc.Health != nil {
				t.Errorf("anthropic Health = %+v, want nil", rc.Health)
			}
			if (rc.Anthropic == shared) != tt.wantShared {
				t.Errorf("reused shared client = %v, want %v", rc.Anthropic == shared, tt.wantShared)
			}
		})
	}
}

// TestBuildClientsSortsResourceClientsIntoTheBundle pins how BuildClients
// files each constructed client: every resource is routable, endpoint
// resources are watched and typed, and every anthropic resource shares
// one client.
func TestBuildClientsSortsResourceClientsIntoTheBundle(t *testing.T) {
	t.Parallel()

	cat := &Catalog{Resources: []Resource{
		{ID: "local-o", Provider: "ollama", URL: "http://127.0.0.1:1"},
		{ID: "local-l", Provider: "lmstudio", URL: "http://127.0.0.1:2"},
		{ID: "local-c", Provider: "openai_compat", URL: "http://127.0.0.1:3"},
		{ID: "cloud-a", Provider: "anthropic"},
		{ID: "cloud-b", Provider: "anthropic"},
	}}
	if err := cat.reindex("", ""); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	cfg := &config.Config{Anthropic: config.AnthropicConfig{APIKey: "test-key"}}

	bundle, err := BuildClients(cat, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildClients: %v", err)
	}

	keys := func(m any) []string {
		var out []string
		for _, k := range reflect.ValueOf(m).MapKeys() {
			out = append(out, k.String())
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{"ResourceClients", keys(bundle.ResourceClients), []string{"cloud-a", "cloud-b", "local-c", "local-l", "local-o"}},
		{"HealthClients", keys(bundle.HealthClients), []string{"local-c", "local-l", "local-o"}},
		{"OllamaClients", keys(bundle.OllamaClients), []string{"local-o"}},
		{"LMStudioClients", keys(bundle.LMStudioClients), []string{"local-l"}},
		{"OpenAICompatClients", keys(bundle.OpenAICompatClients), []string{"local-c"}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s keys = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	if bundle.AnthropicClient == nil {
		t.Fatal("AnthropicClient = nil, want the shared client")
	}
	for _, id := range []string{"cloud-a", "cloud-b"} {
		if bundle.ResourceClients[id] != llm.Client(bundle.AnthropicClient) {
			t.Errorf("ResourceClients[%q] is not the shared anthropic client", id)
		}
	}
	if bundle.ResourceClients["local-l"] != llm.Client(bundle.LMStudioClients["local-l"]) {
		t.Error("lmstudio resource client and typed handle differ")
	}
	if bundle.Client == nil {
		t.Error("routed Client = nil")
	}
}

// TestBuildClientsRefusesAnUnbuildableResource pins that one resource
// NewResourceClient refuses fails the whole bundle, as before.
func TestBuildClientsRefusesAnUnbuildableResource(t *testing.T) {
	t.Parallel()

	cat := &Catalog{Resources: []Resource{
		{ID: "local-o", Provider: "ollama", URL: "http://127.0.0.1:1"},
		{ID: "cloud-a", Provider: "anthropic"},
	}}
	if err := cat.reindex("", ""); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	_, err := BuildClients(cat, &config.Config{}, nil)
	if err == nil || err.Error() != `resource "cloud-a" requires anthropic config` {
		t.Fatalf("BuildClients err = %v, want the anthropic config refusal", err)
	}
}
