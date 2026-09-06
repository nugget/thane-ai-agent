package companions

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func bindingTestStore(t *testing.T, contactByAccount map[string]string) *Store {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, nil, WithContactForAccount(func(account string) string {
		return contactByAccount[account]
	}))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

const (
	bindingContactA = "019c76e4-2ff1-7918-8d6f-6c2488f5098d"
	bindingContactB = "019cd331-9ed4-7294-9177-c982db7c8196"
)

// TestRegistrationStampsTheConfiguredContact is the point of moving the
// binding onto the row: a device that pairs while the process is running
// joins its person immediately, with no restart and no config re-read.
func TestRegistrationStampsTheConfiguredContact(t *testing.T) {
	store := bindingTestStore(t, map[string]string{"nugget": bindingContactA})
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{ClientName: "Nugget's iPhone"}, now); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}
	devices, err := store.DevicesForContact(ctx, bindingContactA)
	if err != nil {
		t.Fatalf("DevicesForContact: %v", err)
	}
	if len(devices) != 1 || devices[0].ClientID != "iphone-1" {
		t.Fatalf("devices = %+v, want the freshly paired iphone-1", devices)
	}
	if devices[0].ContactID != bindingContactA {
		t.Errorf("ContactID = %q, want %q", devices[0].ContactID, bindingContactA)
	}
}

// TestUnclaimedAccountBindsNothing covers the account the operator never
// declared: its devices exist and belong to nobody, rather than to a
// zero-value contact that a query for "" would sweep up.
func TestUnclaimedAccountBindsNothing(t *testing.T) {
	store := bindingTestStore(t, map[string]string{"nugget": bindingContactA})
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.RecordConnected(ctx, "guest", "ipad-1", companion.DeviceMetadata{}, now); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}
	if devices, err := store.DevicesForContact(ctx, ""); err != nil || len(devices) != 0 {
		t.Fatalf("DevicesForContact(\"\") = %+v, %v; an empty contact must match nothing", devices, err)
	}
	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ContactID != "" {
		t.Fatalf("device = %+v, want one unbound row", all)
	}
}

// TestReconcileFollowsConfigInBothDirections covers the rows registration
// cannot reach: written before the column existed, or belonging to an
// account whose declared contact changed while the device was offline.
// Unbinding matters as much as binding — an account the operator stopped
// claiming must not leave its devices pointing at the old person.
func TestReconcileFollowsConfigInBothDirections(t *testing.T) {
	contactByAccount := map[string]string{"nugget": bindingContactA}
	db, err := database.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, nil, WithContactForAccount(func(a string) string { return contactByAccount[a] }))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{}, now); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}

	// The operator reassigns the account to a different person.
	contactByAccount["nugget"] = bindingContactB
	changed, err := store.ReconcileContactBindings(ctx, []string{"nugget"})
	if err != nil {
		t.Fatalf("ReconcileContactBindings: %v", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	if devices, _ := store.DevicesForContact(ctx, bindingContactA); len(devices) != 0 {
		t.Errorf("old contact still owns %d devices", len(devices))
	}
	if devices, _ := store.DevicesForContact(ctx, bindingContactB); len(devices) != 1 {
		t.Errorf("new contact owns %d devices, want 1", len(devices))
	}

	// And the operator stops claiming the account entirely.
	delete(contactByAccount, "nugget")
	if _, err := store.ReconcileContactBindings(ctx, []string{"nugget"}); err != nil {
		t.Fatalf("ReconcileContactBindings unbind: %v", err)
	}
	if devices, _ := store.DevicesForContact(ctx, bindingContactB); len(devices) != 0 {
		t.Errorf("unclaimed account left %d devices bound", len(devices))
	}

	// A reconcile that changes nothing reports nothing, so the startup
	// log stays quiet on an unchanged deployment.
	if changed, err := store.ReconcileContactBindings(ctx, []string{"nugget"}); err != nil || changed != 0 {
		t.Errorf("idempotent reconcile = %d, %v; want 0, nil", changed, err)
	}
}

// TestDevicesForContactExcludesRetiredHardware keeps a deregistered
// device out of the live source inventory.
func TestDevicesForContactExcludesRetiredHardware(t *testing.T) {
	store := bindingTestStore(t, map[string]string{"nugget": bindingContactA})
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "nugget", "iphone-old", companion.DeviceMetadata{}, now); err != nil {
		t.Fatalf("RecordConnected: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE companion_devices SET state = 'retired' WHERE client_id = ?`, "iphone-old"); err != nil {
		t.Fatalf("retire device: %v", err)
	}
	devices, err := store.DevicesForContact(ctx, bindingContactA)
	if err != nil {
		t.Fatalf("DevicesForContact: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("retired device still listed: %+v", devices)
	}
}

// TestReconnectRefreshesAStaleBinding covers the device that was already
// known when its account changed hands. Registration is an upsert, so a
// returning phone must adopt the account's current contact rather than
// keeping the one it was first stamped with — otherwise a reassignment
// only takes effect for hardware that has never connected before.
func TestReconnectRefreshesAStaleBinding(t *testing.T) {
	contactByAccount := map[string]string{"nugget": bindingContactA}
	db, err := database.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, nil, WithContactForAccount(func(a string) string { return contactByAccount[a] }))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{}, now); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	contactByAccount["nugget"] = bindingContactB
	if err := store.RecordConnected(ctx, "nugget", "iphone-1", companion.DeviceMetadata{}, now.Add(time.Minute)); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	if devices, _ := store.DevicesForContact(ctx, bindingContactA); len(devices) != 0 {
		t.Errorf("reconnect kept the stale binding: %d devices still on the old contact", len(devices))
	}
	devices, err := store.DevicesForContact(ctx, bindingContactB)
	if err != nil {
		t.Fatalf("DevicesForContact: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("new contact owns %d devices, want 1", len(devices))
	}
}
