package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	emailcfg "github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
	"github.com/nugget/thane-ai-agent/internal/integrations/unifi"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/curator"
	"github.com/nugget/thane-ai-agent/internal/runtime/ego"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
)

// coreServiceTestConfig returns a config with all three model-facing
// core service loops enabled and valid sleep envelopes, mirroring what
// applyDefaults stamps in production (which the app-package test can't
// call directly since it's unexported in the config package).
func coreServiceTestConfig() *config.Config {
	return &config.Config{
		Metacognitive: config.MetacognitiveConfig{
			Enabled: true, MinSleep: "2m", MaxSleep: "30m", DefaultSleep: "10m",
		},
		Ego: config.EgoConfig{
			Enabled: true, MinSleep: "30m", MaxSleep: "24h", DefaultSleep: "6h",
		},
		Curator: config.CuratorConfig{
			Enabled: true, MinSleep: "15m", MaxSleep: "12h", DefaultSleep: "1h",
		},
	}
}

type testDeviceLocator struct {
	locations []unifi.DeviceLocation
}

func (l testDeviceLocator) LocateDevices(context.Context) ([]unifi.DeviceLocation, error) {
	return append([]unifi.DeviceLocation(nil), l.locations...), nil
}

func (l testDeviceLocator) Ping(context.Context) error { return nil }

type testRoomUpdater struct {
	updates int
}

func (u *testRoomUpdater) UpdateRoom(entityID, room, source string) {
	u.updates++
}

func TestBuildLoopDefinitionBaseSpecs_AppendsConfiguredBuiltIns(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		HomeAssistant: config.HomeAssistantConfig{
			URL:   "http://ha.local",
			Token: "token",
		},
		Unifi: config.UnifiConfig{
			URL:             "https://unifi.local",
			APIKey:          "key",
			PollIntervalSec: 30,
		},
		Person: config.PersonConfig{
			Track: []string{"person.dan"},
		},
		Email: emailcfg.Config{
			PollIntervalSec: 300,
			Accounts: []emailcfg.AccountConfig{{
				Name: "personal",
				IMAP: emailcfg.IMAPConfig{
					Host:     "imap.example.com",
					Username: "dan@example.com",
				},
			}},
		},
		Media: config.MediaConfig{
			FeedCheckInterval: 600,
		},
		Forge: forge.Config{
			SubscriptionCheckInterval: 900,
			Accounts: []forge.AccountConfig{{
				Name:     "github",
				Provider: "github",
				Token:    "token",
			}},
		},
		MQTT: config.MQTTConfig{
			Broker:             "mqtt://broker.local:1883",
			DeviceName:         "thane-dev",
			PublishIntervalSec: 60,
			Telemetry: config.TelemetryConfig{
				Enabled:  true,
				Interval: 60,
			},
		},
	}

	a := &App{cfg: cfg}
	specs, err := a.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}

	want := map[string]bool{
		unifiPollerDefinitionName:     false,
		haStateWatcherDefinitionName:  false,
		emailPollerDefinitionName:     false,
		forgeSubPollerDefinitionName:  false,
		mediaFeedPollerDefinitionName: false,
		mqttPublisherDefinitionName:   false,
		telemetryDefinitionName:       false,
	}
	for _, spec := range specs {
		if _, ok := want[spec.Name]; ok {
			want[spec.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("definition %q missing from built-in base specs", name)
		}
	}
}

// TestMediaDefaultHandlerSpecIsCloudEligible pins the routing
// regression fix: when PR-T2c retired mediaFeedTurnBuilder it dropped
// the LocalOnly=false / QualityFloor=5 stamping the TurnBuilder did
// on every feed turn. Without those, the agent runtime defaults
// local_only=true on non-supervisor turns and feed triage routes to
// local-only models — a real behavior regression for transcript
// summarization. The default handler's Profile must carry both.
func TestMediaDefaultHandlerSpecIsCloudEligible(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Media: config.MediaConfig{FeedCheckInterval: 600},
	}
	specs := builtInServiceDefinitionSpecs(cfg)
	var got *looppkg.Spec
	for i := range specs {
		if specs[i].Name == media.DefaultHandlerLoopName {
			got = &specs[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("%s definition missing from built-in specs", media.DefaultHandlerLoopName)
	}
	if got.Profile.LocalOnly != "false" {
		t.Errorf("Profile.LocalOnly = %q, want \"false\" (cloud-eligible)", got.Profile.LocalOnly)
	}
	if got.Profile.QualityFloor < 1 {
		t.Errorf("Profile.QualityFloor = %d, want non-zero (was 5 on the retired TurnBuilder)", got.Profile.QualityFloor)
	}
}

func TestBuildLoopDefinitionBaseSpecs_SkipsDuplicateBuiltinNames(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MQTT: config.MQTTConfig{
			Broker:             "mqtt://broker.local:1883",
			DeviceName:         "thane-dev",
			PublishIntervalSec: 60,
		},
		Loops: config.LoopsConfig{
			Definitions: []looppkg.Spec{{
				Name:       mqttPublisherDefinitionName,
				Enabled:    true,
				Task:       "Custom mqtt publisher definition.",
				Operation:  looppkg.OperationService,
				Completion: looppkg.CompletionNone,
			}},
		},
	}

	a := &App{cfg: cfg}
	specs, err := a.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}

	var count int
	for _, spec := range specs {
		if spec.Name == mqttPublisherDefinitionName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mqtt definition count = %d, want 1", count)
	}
}

// TestBuildLoopDefinitionBaseSpecs_AppendsCoreServiceLoops covers the
// coreServiceLoops registration path: enabled ego/metacognitive/curator
// definitions are appended and their parsed configs cached for later
// hydration. Replaces the assertions the three hand-written blocks used
// to carry before #988 collapsed them behind one descriptor slice.
func TestBuildLoopDefinitionBaseSpecs_AppendsCoreServiceLoops(t *testing.T) {
	t.Parallel()

	a := &App{cfg: coreServiceTestConfig()}
	specs, err := a.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}

	want := map[string]bool{
		metacognitive.DefinitionName: false,
		ego.DefinitionName:           false,
		curator.DefinitionName:       false,
	}
	for _, spec := range specs {
		if _, ok := want[spec.Name]; ok {
			want[spec.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("core service definition %q missing from base specs", name)
		}
	}

	// Each enabled core loop must cache its parsed config so
	// hydrateLoopDefinitionSpec can attach runtime hooks later.
	if a.metacogCfg == nil {
		t.Error("metacogCfg not cached after build")
	}
	if a.egoCfg == nil {
		t.Error("egoCfg not cached after build")
	}
	if a.curatorCfg == nil {
		t.Error("curatorCfg not cached after build")
	}
}

// TestCoreServiceLoopByNameMatchesSlice guards the invariant that the
// hydration dispatch index stays in lockstep with the registration
// slice — and that all three model-facing core loops are registered.
func TestCoreServiceLoopByNameMatchesSlice(t *testing.T) {
	t.Parallel()

	if len(coreServiceLoopByName) != len(coreServiceLoops) {
		t.Fatalf("index size %d != slice size %d", len(coreServiceLoopByName), len(coreServiceLoops))
	}
	for _, reg := range coreServiceLoops {
		indexed, ok := coreServiceLoopByName[reg.Name]
		if !ok {
			t.Errorf("registration %q missing from dispatch index", reg.Name)
			continue
		}
		if indexed.Name != reg.Name {
			t.Errorf("index[%q].Name = %q", reg.Name, indexed.Name)
		}
	}
	for _, name := range []string{ego.DefinitionName, metacognitive.DefinitionName, curator.DefinitionName} {
		if _, ok := coreServiceLoopByName[name]; !ok {
			t.Errorf("core loop %q not registered in coreServiceLoops", name)
		}
	}
}

// TestCoreServiceRegistrationHydrate exercises each registration's
// Hydrate closure directly: a missing cached config is a hard error,
// and a hydrated spec carries the runtime task builder. Driving the
// closures (rather than hydrateLoopDefinitionSpec) keeps the test
// focused on the #988 dispatch contract without pulling in the
// document-output hydration machinery.
func TestCoreServiceRegistrationHydrate(t *testing.T) {
	t.Parallel()

	cfg := coreServiceTestConfig()
	for _, reg := range coreServiceLoops {
		t.Run(reg.Name, func(t *testing.T) {
			// Missing cached config: a definition can't hydrate
			// without the config that shapes its runtime behavior.
			bare := &App{cfg: cfg}
			if _, err := reg.Hydrate(bare, looppkg.Spec{Name: reg.Name}); err == nil {
				t.Fatalf("Hydrate without cached config: expected error, got nil")
			}

			a := &App{cfg: cfg}
			if err := reg.ParseAndCache(a, cfg); err != nil {
				t.Fatalf("ParseAndCache: %v", err)
			}
			spec, err := reg.Hydrate(a, looppkg.Spec{Name: reg.Name})
			if err != nil {
				t.Fatalf("Hydrate: %v", err)
			}
			if spec.TaskBuilder == nil {
				t.Error("Hydrate did not attach TaskBuilder")
			}
		})
	}
}

func TestHydrateLoopDefinitionSpec_UnifiPoller(t *testing.T) {
	t.Parallel()

	updater := &testRoomUpdater{}
	poller := unifi.NewPoller(unifi.PollerConfig{
		Locator: testDeviceLocator{locations: []unifi.DeviceLocation{{
			MAC:      "aa:bb:cc:dd:ee:ff",
			APName:   "office-ap",
			LastSeen: time.Now().Unix(),
		}}},
		Updater:      updater,
		PollInterval: 30 * time.Second,
		DeviceOwners: map[string]string{"aa:bb:cc:dd:ee:ff": "person.dan"},
		APRooms:      map[string]string{"office-ap": "office"},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	a := &App{unifiPoller: poller}
	spec, err := a.hydrateLoopDefinitionSpec(looppkg.Spec{Name: unifiPollerDefinitionName})
	if err != nil {
		t.Fatalf("hydrateLoopDefinitionSpec: %v", err)
	}
	if spec.Handler == nil {
		t.Fatal("Handler should be set for unifi poller definition")
	}
	if err := spec.Handler(context.Background(), nil); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if updater.updates != 0 {
		t.Fatalf("updates = %d, want 0 on first debounce pass", updater.updates)
	}
}

func TestHydrateLoopDefinitionSpec_HAStateWatcher(t *testing.T) {
	t.Parallel()

	eventsCh := make(chan homeassistant.Event, 1)
	var handled int
	watcher := homeassistant.NewStateWatcher(eventsCh, nil, nil, func(entityID, oldState, newState string) {
		handled++
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	a := &App{haStateWatcher: watcher}
	spec, err := a.hydrateLoopDefinitionSpec(looppkg.Spec{Name: haStateWatcherDefinitionName})
	if err != nil {
		t.Fatalf("hydrateLoopDefinitionSpec: %v", err)
	}
	if spec.WaitFunc == nil || spec.Handler == nil {
		t.Fatalf("hydrated spec = %+v, want WaitFunc and Handler", spec)
	}

	payload, err := json.Marshal(homeassistant.StateChangedData{
		EntityID: "light.office",
		OldState: &homeassistant.State{State: "off"},
		NewState: &homeassistant.State{State: "on"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	eventsCh <- homeassistant.Event{
		Type: "state_changed",
		Data: payload,
	}

	eventBatch, err := spec.WaitFunc(context.Background())
	if err != nil {
		t.Fatalf("WaitFunc: %v", err)
	}
	if err := spec.Handler(context.Background(), eventBatch); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if handled != 1 {
		t.Fatalf("handled = %d, want 1", handled)
	}
}
