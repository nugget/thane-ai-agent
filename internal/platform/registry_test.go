package platform

import (
	"sync"
	"testing"
	"time"
)

func TestRegistryAddRemove(t *testing.T) {
	r := NewRegistry(nil)

	p := &Provider{
		ID:          "prov_test1",
		ClientName:  "Test Mac",
		ClientID:    "uuid-1",
		ConnectedAt: time.Now(),
		done:        make(chan struct{}),
	}

	r.Add(p)
	if got := r.Count(); got != 1 {
		t.Fatalf("Count after Add: got %d, want 1", got)
	}

	infos := r.List()
	if len(infos) != 1 {
		t.Fatalf("List length: got %d, want 1", len(infos))
	}
	if infos[0].ID != "prov_test1" {
		t.Errorf("provider ID: got %q, want %q", infos[0].ID, "prov_test1")
	}
	if infos[0].ClientName != "Test Mac" {
		t.Errorf("client name: got %q, want %q", infos[0].ClientName, "Test Mac")
	}

	r.Remove("prov_test1")
	if got := r.Count(); got != 0 {
		t.Fatalf("Count after Remove: got %d, want 0", got)
	}
}

func TestRegistryRemoveNonexistent(t *testing.T) {
	r := NewRegistry(nil)
	// Should not panic.
	r.Remove("prov_doesnotexist")
}

func TestRegistryConcurrency(t *testing.T) {
	r := NewRegistry(nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := &Provider{
				ID:          generateProviderID(),
				ClientName:  "concurrent",
				ClientID:    "uuid",
				ConnectedAt: time.Now(),
				done:        make(chan struct{}),
			}
			r.Add(p)
			_ = r.Count()
			_ = r.List()
			r.Remove(p.ID)
		}(i)
	}
	wg.Wait()

	if got := r.Count(); got != 0 {
		t.Errorf("Count after concurrent add/remove: got %d, want 0", got)
	}
}

func TestGenerateProviderID(t *testing.T) {
	id := generateProviderID()
	if len(id) < 6 {
		t.Errorf("provider ID too short: %q", id)
	}
	if id[:5] != "prov_" {
		t.Errorf("provider ID missing prefix: %q", id)
	}

	// IDs should be unique.
	seen := make(map[string]bool)
	for range 100 {
		pid := generateProviderID()
		if seen[pid] {
			t.Fatalf("duplicate provider ID: %q", pid)
		}
		seen[pid] = true
	}
}
