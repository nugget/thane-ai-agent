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
		Account:     "nugget",
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
	if infos[0].Account != "nugget" {
		t.Errorf("account: got %q, want %q", infos[0].Account, "nugget")
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

func TestRegistryByAccount(t *testing.T) {
	r := NewRegistry(nil)

	// Two providers under "nugget", one under "aimee".
	nugget1 := &Provider{
		ID: "prov_n1", Account: "nugget",
		ClientName: "deepslate", ClientID: "uuid-n1",
		ConnectedAt: time.Now(), done: make(chan struct{}),
	}
	nugget2 := &Provider{
		ID: "prov_n2", Account: "nugget",
		ClientName: "granite", ClientID: "uuid-n2",
		ConnectedAt: time.Now(), done: make(chan struct{}),
	}
	aimee := &Provider{
		ID: "prov_a1", Account: "aimee",
		ClientName: "pocket", ClientID: "uuid-a1",
		ConnectedAt: time.Now(), done: make(chan struct{}),
	}

	r.Add(nugget1)
	r.Add(nugget2)
	r.Add(aimee)

	nuggetProviders := r.ByAccount("nugget")
	if len(nuggetProviders) != 2 {
		t.Fatalf("ByAccount(nugget): got %d, want 2", len(nuggetProviders))
	}

	aimeeProviders := r.ByAccount("aimee")
	if len(aimeeProviders) != 1 {
		t.Fatalf("ByAccount(aimee): got %d, want 1", len(aimeeProviders))
	}
	if aimeeProviders[0].ClientName != "pocket" {
		t.Errorf("aimee client: got %q, want %q", aimeeProviders[0].ClientName, "pocket")
	}

	// No providers for unknown account.
	if got := r.ByAccount("unknown"); got != nil {
		t.Errorf("ByAccount(unknown): got %v, want nil", got)
	}

	// Remove one nugget provider — account index should shrink.
	r.Remove("prov_n1")
	nuggetProviders = r.ByAccount("nugget")
	if len(nuggetProviders) != 1 {
		t.Fatalf("ByAccount(nugget) after remove: got %d, want 1", len(nuggetProviders))
	}

	// Remove the last nugget provider — account should disappear.
	r.Remove("prov_n2")
	if got := r.ByAccount("nugget"); got != nil {
		t.Errorf("ByAccount(nugget) after all removed: got %v, want nil", got)
	}

	accounts := r.Accounts()
	if len(accounts) != 1 || accounts[0] != "aimee" {
		t.Errorf("Accounts: got %v, want [aimee]", accounts)
	}
}

func TestRegistryConcurrency(t *testing.T) {
	r := NewRegistry(nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			account := "even"
			if n%2 != 0 {
				account = "odd"
			}
			p := &Provider{
				ID:          generateProviderID(),
				Account:     account,
				ClientName:  "concurrent",
				ClientID:    "uuid",
				ConnectedAt: time.Now(),
				done:        make(chan struct{}),
			}
			r.Add(p)
			_ = r.Count()
			_ = r.List()
			_ = r.ByAccount(account)
			_ = r.Accounts()
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
