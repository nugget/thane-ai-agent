package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/documents"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

type capturedMQTTPublish struct {
	Topic   string
	Payload []byte
	Retain  bool
}

type fakeMQTTPublisher struct {
	publishes []capturedMQTTPublish
}

func (f *fakeMQTTPublisher) PublishTopic(_ context.Context, topic string, payload []byte, retain bool) error {
	f.publishes = append(f.publishes, capturedMQTTPublish{
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
		Retain:  retain,
	})
	return nil
}

func TestLoopOutputDispatcherDeliversObservationTargets(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	observations, err := newLoopObservationStore(db)
	if err != nil {
		t.Fatalf("newLoopObservationStore: %v", err)
	}

	generatedDir := filepath.Join(t.TempDir(), "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(generatedDir): %v", err)
	}
	docStore, err := documents.NewStore(db, map[string]string{"generated": generatedDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}

	mqtt := &fakeMQTTPublisher{}
	dispatcher := newLoopOutputDispatcher(observations, docStore, nil, nil)
	dispatcher.publishMQTT = mqtt.PublishTopic

	observation := looppkg.Observation{
		Timestamp:  time.Date(2026, 4, 9, 12, 34, 56, 0, time.UTC),
		LoopID:     "loop-123",
		LoopName:   "battery_watch",
		Operation:  looppkg.OperationService,
		Iteration:  7,
		RequestID:  "r_test",
		Model:      "claude-opus",
		Content:    "Shed battery dropped below 20%.",
		Summary:    map[string]any{"severity": "warning"},
		Supervisor: true,
		ActiveTags: []string{"ha", "loops"},
	}

	err = dispatcher.Deliver(context.Background(), looppkg.OutputDelivery{
		Targets: []looppkg.OutputTarget{
			{Kind: looppkg.OutputTargetObservationLog},
			{
				Kind:       looppkg.OutputTargetDocumentJournal,
				Ref:        "generated:loop-journals/battery-watch.md",
				Window:     "daily",
				MaxWindows: 7,
				Title:      "Battery Watch",
			},
			{Kind: looppkg.OutputTargetMQTTTopic, Topic: "thane/test/loop-observations", Retain: true},
		},
		Observation: observation,
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM loop_observations`).Scan(&count); err != nil {
		t.Fatalf("count loop_observations: %v", err)
	}
	if count != 1 {
		t.Fatalf("loop_observations count = %d, want 1", count)
	}

	doc, err := docStore.Read(context.Background(), "generated:loop-journals/battery-watch.md")
	if err != nil {
		t.Fatalf("Read journal doc: %v", err)
	}
	if !strings.Contains(doc.Body, "## 2026-04-09") || !strings.Contains(doc.Body, "Shed battery dropped below 20%.") {
		t.Fatalf("journal body = %q, want dated journal window with observation text", doc.Body)
	}
	if !strings.Contains(doc.Body, "Context: loop=battery_watch; iteration=7; request_id=r_test; model=claude-opus; supervisor=true") {
		t.Fatalf("journal body = %q, want context footer", doc.Body)
	}

	if len(mqtt.publishes) != 1 {
		t.Fatalf("mqtt publishes = %d, want 1", len(mqtt.publishes))
	}
	if mqtt.publishes[0].Topic != "thane/test/loop-observations" || !mqtt.publishes[0].Retain {
		t.Fatalf("mqtt publish = %#v, want retained topic publish", mqtt.publishes[0])
	}
	var gotMQTT looppkg.Observation
	if err := json.Unmarshal(mqtt.publishes[0].Payload, &gotMQTT); err != nil {
		t.Fatalf("unmarshal mqtt payload: %v", err)
	}
	if gotMQTT.LoopName != "battery_watch" || gotMQTT.Content != "Shed battery dropped below 20%." {
		t.Fatalf("mqtt payload = %#v, want loop/content preserved", gotMQTT)
	}
}
