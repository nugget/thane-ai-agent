package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	emailcfg "github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/unifi"
)

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
