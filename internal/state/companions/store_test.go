package companions

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

var (
	t0 = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(1 * time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func TestRecordConnectedCreatesDevice(t *testing.T) {
	store := newTestStore(t)

	meta := companion.DeviceMetadata{
		ClientName: "Alice's iPhone",
		Platform:   "ios",
		AppVersion: "1.2.0",
		OSVersion:  "26.0",
	}
	if err := store.RecordConnected("alice", "device-1", meta, t0); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}

	d, ok, err := store.Get("alice", "device-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if d.ClientName != meta.ClientName || d.Platform != "ios" || d.AppVersion != "1.2.0" || d.OSVersion != "26.0" {
		t.Errorf("metadata mismatch: %+v", d)
	}
	if !d.FirstSeenAt.Equal(t0) || !d.LastSeenAt.Equal(t0) || !d.LastConnectedAt.Equal(t0) {
		t.Errorf("timestamps mismatch: first=%v last=%v connected=%v", d.FirstSeenAt, d.LastSeenAt, d.LastConnectedAt)
	}
	if !d.LastDisconnectedAt.IsZero() {
		t.Errorf("never-disconnected device has LastDisconnectedAt=%v", d.LastDisconnectedAt)
	}
	if d.State != DeviceStateActive {
		t.Errorf("state = %q, want %q", d.State, DeviceStateActive)
	}
	if string(d.Capabilities) != "[]" {
		t.Errorf("fresh device capabilities = %q, want []", d.Capabilities)
	}
}

func TestRecordConnectedPreservesFirstSeenAndMetadata(t *testing.T) {
	store := newTestStore(t)

	full := companion.DeviceMetadata{ClientName: "Alice's Mac", Platform: "macos", AppVersion: "1.0", OSVersion: "15.6"}
	if err := store.RecordConnected("alice", "device-1", full, t0); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	// Reconnect reporting only a new app version: absent fields must not
	// erase what the device said before; the present one must update.
	partial := companion.DeviceMetadata{AppVersion: "1.1"}
	if err := store.RecordConnected("alice", "device-1", partial, t1); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	d, ok, err := store.Get("alice", "device-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !d.FirstSeenAt.Equal(t0) {
		t.Errorf("FirstSeenAt = %v, want preserved %v", d.FirstSeenAt, t0)
	}
	if !d.LastConnectedAt.Equal(t1) || !d.LastSeenAt.Equal(t1) {
		t.Errorf("reconnect timestamps not updated: connected=%v seen=%v", d.LastConnectedAt, d.LastSeenAt)
	}
	if d.ClientName != "Alice's Mac" || d.Platform != "macos" || d.OSVersion != "15.6" {
		t.Errorf("absent metadata fields were erased: %+v", d)
	}
	if d.AppVersion != "1.1" {
		t.Errorf("AppVersion = %q, want updated 1.1", d.AppVersion)
	}
}

func TestRecordCapabilities(t *testing.T) {
	store := newTestStore(t)
	if err := store.RecordConnected("alice", "device-1", companion.DeviceMetadata{}, t0); err != nil {
		t.Fatalf("connect: %v", err)
	}

	manifest := []byte(`[{"name":"location","methods":["current"]}]`)
	if err := store.RecordCapabilities("alice", "device-1", manifest, t1); err != nil {
		t.Fatalf("RecordCapabilities: %v", err)
	}

	d, _, err := store.Get("alice", "device-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(d.Capabilities) != string(manifest) {
		t.Errorf("capabilities = %s, want %s", d.Capabilities, manifest)
	}
	if !d.LastSeenAt.Equal(t1) {
		t.Errorf("LastSeenAt = %v, want bumped to %v", d.LastSeenAt, t1)
	}

	if err := store.RecordCapabilities("alice", "device-1", nil, t2); err != nil {
		t.Fatalf("empty manifest: %v", err)
	}
	d, _, _ = store.Get("alice", "device-1")
	if string(d.Capabilities) != "[]" {
		t.Errorf("empty manifest stored as %q, want []", d.Capabilities)
	}
}

func TestRecordCapabilitiesRejectsInvalid(t *testing.T) {
	store := newTestStore(t)
	if err := store.RecordConnected("alice", "device-1", companion.DeviceMetadata{}, t0); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := store.RecordCapabilities("alice", "device-1", []byte("{not json"), t1); err == nil {
		t.Error("invalid JSON manifest accepted")
	}
	if err := store.RecordCapabilities("alice", "unknown-device", []byte("[]"), t1); err == nil {
		t.Error("capabilities for unrecorded device accepted")
	}
}

func TestRecordDisconnectedPreservesRecord(t *testing.T) {
	store := newTestStore(t)
	meta := companion.DeviceMetadata{ClientName: "Alice's iPhone", Platform: "ios"}
	if err := store.RecordConnected("alice", "device-1", meta, t0); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := store.RecordDisconnected("alice", "device-1", t1); err != nil {
		t.Fatalf("RecordDisconnected: %v", err)
	}

	d, ok, err := store.Get("alice", "device-1")
	if err != nil || !ok {
		t.Fatalf("device deleted by disconnect: ok=%v err=%v", ok, err)
	}
	if !d.LastDisconnectedAt.Equal(t1) || !d.LastSeenAt.Equal(t1) {
		t.Errorf("disconnect timestamps: disconnected=%v seen=%v, want both %v", d.LastDisconnectedAt, d.LastSeenAt, t1)
	}
	if !d.LastConnectedAt.Equal(t0) {
		t.Errorf("LastConnectedAt = %v, want preserved %v", d.LastConnectedAt, t0)
	}
	if d.ClientName != "Alice's iPhone" {
		t.Errorf("metadata degraded on disconnect: %+v", d)
	}

	if err := store.RecordDisconnected("alice", "unknown-device", t1); err == nil {
		t.Error("disconnect for unrecorded device accepted")
	}
}

func TestKeyValidation(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name     string
		account  string
		clientID string
	}{
		{"empty account", "", "device-1"},
		{"empty client_id", "alice", ""},
		{"whitespace client_id", "alice", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.RecordConnected(tc.account, tc.clientID, companion.DeviceMetadata{}, t0); err == nil {
				t.Error("RecordConnected accepted invalid key")
			}
			if err := store.RecordCapabilities(tc.account, tc.clientID, []byte("[]"), t0); err == nil {
				t.Error("RecordCapabilities accepted invalid key")
			}
			if err := store.RecordDisconnected(tc.account, tc.clientID, t0); err == nil {
				t.Error("RecordDisconnected accepted invalid key")
			}
		})
	}
}

func TestListOrdersByAccountThenClient(t *testing.T) {
	store := newTestStore(t)
	for _, key := range [][2]string{{"bob", "device-2"}, {"alice", "device-2"}, {"alice", "device-1"}} {
		if err := store.RecordConnected(key[0], key[1], companion.DeviceMetadata{}, t0); err != nil {
			t.Fatalf("connect %v: %v", key, err)
		}
	}
	devices, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got [][2]string
	for _, d := range devices {
		got = append(got, [2]string{d.Account, d.ClientID})
	}
	want := [][2]string{{"alice", "device-1"}, {"alice", "device-2"}, {"bob", "device-2"}}
	if len(got) != len(want) {
		t.Fatalf("List returned %d devices, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestSurvivesReopen is the restart-survival contract (#1437): known
// companion identity persists across a process restart, and the schema
// migration is idempotent on the second open.
func TestSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thane.db")

	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	meta := companion.DeviceMetadata{ClientName: "Alice's iPhone", Platform: "ios", AppVersion: "1.2.0"}
	if err := store.RecordConnected("alice", "device-1", meta, t0); err != nil {
		t.Fatalf("connect: %v", err)
	}
	manifest := []byte(`[{"name":"location"}]`)
	if err := store.RecordCapabilities("alice", "device-1", manifest, t0); err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if err := store.RecordDisconnected("alice", "device-1", t1); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	store2, err := NewStore(db2, nil)
	if err != nil {
		t.Fatalf("second migration not idempotent: %v", err)
	}

	d, ok, err := store2.Get("alice", "device-1")
	if err != nil || !ok {
		t.Fatalf("device lost across restart: ok=%v err=%v", ok, err)
	}
	if d.ClientName != "Alice's iPhone" || d.Platform != "ios" || d.AppVersion != "1.2.0" {
		t.Errorf("metadata lost across restart: %+v", d)
	}
	if !d.FirstSeenAt.Equal(t0) || !d.LastDisconnectedAt.Equal(t1) {
		t.Errorf("timestamps lost across restart: first=%v disconnected=%v", d.FirstSeenAt, d.LastDisconnectedAt)
	}
	var caps []map[string]any
	if err := json.Unmarshal(d.Capabilities, &caps); err != nil || len(caps) != 1 {
		t.Errorf("capabilities lost across restart: %s (err=%v)", d.Capabilities, err)
	}
}
