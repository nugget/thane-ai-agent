package anticipation

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestProvider_ConcurrentAccess(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	provider := NewProvider(store)

	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3) // writers, readers, clearers

	// Concurrent writers via SetWakeContext.
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				provider.SetWakeContext(WakeContext{
					Time:        time.Now(),
					EventType:   "state_change",
					EntityID:    "light.test",
					EntityState: "on",
				})
			}
		}()
	}

	// Concurrent readers via GetContext.
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = provider.GetContext(context.Background(), "test")
			}
		}()
	}

	// Concurrent clearers.
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				provider.ClearWakeContext()
			}
		}()
	}

	wg.Wait()
}

func TestProvider_SetAndGetContext(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Create an anticipation that matches state_change on light.kitchen.
	err = store.Create(&Anticipation{
		ID:          "test-ant",
		Description: "Kitchen light turned on",
		Context:     "The kitchen light was turned on",
		Trigger: Trigger{
			EntityID:    "light.kitchen",
			EntityState: "on",
			EventType:   "state_change",
		},
	})
	if err != nil {
		t.Fatalf("create anticipation: %v", err)
	}

	provider := NewProvider(store)

	// Before setting context, no match.
	result, err := provider.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result before SetWakeContext, got %q", result)
	}

	// Set matching wake context.
	provider.SetWakeContext(WakeContext{
		Time:        time.Now(),
		EventType:   "state_change",
		EntityID:    "light.kitchen",
		EntityState: "on",
	})

	result, err = provider.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result after SetWakeContext")
	}

	// Clear and verify no match.
	provider.ClearWakeContext()
	result, err = provider.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext after clear: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result after ClearWakeContext, got %q", result)
	}
}
