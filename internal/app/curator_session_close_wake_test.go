package app

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/curator"
)

// TestBuildSessionCloseEnvelope_HappyPath verifies the curator wake
// envelope construction: target loop is the curator, payload is the
// event_source shape with one LoopEventPayload carrying source +
// type = "session_close", the session ID as event ID, and the reason
// in metadata.
func TestBuildSessionCloseEnvelope_HappyPath(t *testing.T) {
	const sessionID = "019e6867-00fc-7d6d-88be-58fab5c173c4"
	const reason = "idle_timeout"

	before := time.Now().UTC()
	env, err := buildSessionCloseEnvelope(sessionID, reason)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("buildSessionCloseEnvelope: %v", err)
	}

	// Destination — loop selected by name == "curator".
	if env.To.Kind != messages.DestinationLoop {
		t.Errorf("To.Kind = %q, want loop", env.To.Kind)
	}
	if env.To.Selector != messages.SelectorName {
		t.Errorf("To.Selector = %q, want name", env.To.Selector)
	}
	if env.To.Target != curator.DefinitionName {
		t.Errorf("To.Target = %q, want %q", env.To.Target, curator.DefinitionName)
	}

	// From — system identity from archive_store.
	if env.From.Kind != messages.IdentitySystem {
		t.Errorf("From.Kind = %q, want system", env.From.Kind)
	}
	if env.From.Name != "archive_store" {
		t.Errorf("From.Name = %q, want archive_store", env.From.Name)
	}

	// Payload — event_source shape with one LoopEventPayload.
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want LoopNotifyPayload", env.Payload)
	}
	if payload.Kind != "event_source" {
		t.Errorf("payload.Kind = %q, want event_source", payload.Kind)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("payload.Events len = %d, want 1", len(payload.Events))
	}
	ev := payload.Events[0]
	if ev.Source != "session_close" {
		t.Errorf("event.Source = %q, want session_close", ev.Source)
	}
	if ev.Type != "session_close" {
		t.Errorf("event.Type = %q, want session_close", ev.Type)
	}
	if ev.ID != sessionID {
		t.Errorf("event.ID = %q, want %q", ev.ID, sessionID)
	}
	if ev.Metadata["session_id"] != sessionID {
		t.Errorf("event.Metadata[session_id] = %q, want %q", ev.Metadata["session_id"], sessionID)
	}
	if ev.Metadata["reason"] != reason {
		t.Errorf("event.Metadata[reason] = %q, want %q", ev.Metadata["reason"], reason)
	}
	if ev.ObservedAt.Before(before) || ev.ObservedAt.After(after) {
		t.Errorf("event.ObservedAt = %v, want within [%v, %v]", ev.ObservedAt, before, after)
	}

	// Instructions on the wake — terse-but-pointed nudge the curator
	// prompt's session_close mode references.
	if payload.Context == "" {
		t.Error("payload.Context (instructions) empty — curator wake should carry the 'fold into dossiers' nudge")
	}
}

// TestBuildSessionCloseEnvelope_EmptySessionID rejects malformed
// callers before the envelope reaches the loop runtime — preserving
// the invariant that every wake refers to a real session.
func TestBuildSessionCloseEnvelope_EmptySessionID(t *testing.T) {
	_, err := buildSessionCloseEnvelope("", "test")
	if err == nil {
		t.Fatal("buildSessionCloseEnvelope with empty sessionID should error")
	}
}
