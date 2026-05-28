package homeassistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestClient_GetAreasUsesRegistryCache verifies the getter wiring: a
// repeat call inside the TTL is served from cache (no second HTTP hit),
// invalidation forces a refetch, and a zero TTL disables caching. Uses
// the REST fallback path (no WS client) so the server hit is countable.
func TestClient_GetAreasUsesRegistryCache(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config/area_registry/list" {
			atomic.AddInt32(&hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"area_id":"office","name":"Office"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", nil)
	ctx := context.Background()

	if _, err := client.GetAreas(ctx); err != nil {
		t.Fatalf("GetAreas 1: %v", err)
	}
	if _, err := client.GetAreas(ctx); err != nil {
		t.Fatalf("GetAreas 2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server hits within TTL = %d, want 1 (second call cached)", got)
	}

	client.InvalidateRegistryCache()
	if _, err := client.GetAreas(ctx); err != nil {
		t.Fatalf("GetAreas after invalidate: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("server hits after invalidate = %d, want 2", got)
	}

	client.SetRegistryCacheTTL(0)
	for i := 0; i < 2; i++ {
		if _, err := client.GetAreas(ctx); err != nil {
			t.Fatalf("GetAreas ttl=0 #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Fatalf("server hits with ttl=0 = %d, want 4 (caching disabled)", got)
	}
}

func TestCachedSlice_ReusesWithinTTL(t *testing.T) {
	t.Parallel()

	var calls int
	fetch := func() ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	var c cachedSlice[int]
	base := time.Unix(1000, 0)

	first, err := c.get(time.Minute, base, fetch)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	// 30s later, still within the 1m TTL — must reuse, no refetch.
	second, err := c.get(time.Minute, base.Add(30*time.Second), fetch)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times within TTL, want 1", calls)
	}
	if first[0] != 1 || second[0] != 1 {
		t.Fatalf("cached values = %v/%v, want both from the single fetch", first, second)
	}
}

func TestCachedSlice_RefetchesAfterExpiry(t *testing.T) {
	t.Parallel()

	var calls int
	fetch := func() ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	var c cachedSlice[int]
	base := time.Unix(1000, 0)

	if _, err := c.get(time.Minute, base, fetch); err != nil {
		t.Fatalf("first get: %v", err)
	}
	// Past the TTL — must refetch.
	got, err := c.get(time.Minute, base.Add(90*time.Second), fetch)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if calls != 2 {
		t.Fatalf("fetch called %d times across expiry, want 2", calls)
	}
	if got[0] != 2 {
		t.Fatalf("post-expiry value = %v, want fresh fetch (2)", got)
	}
}

func TestCachedSlice_InvalidateForcesRefetch(t *testing.T) {
	t.Parallel()

	var calls int
	fetch := func() ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	var c cachedSlice[int]
	base := time.Unix(1000, 0)

	if _, err := c.get(time.Minute, base, fetch); err != nil {
		t.Fatalf("first get: %v", err)
	}
	c.invalidate()
	// Still within TTL, but invalidated — must refetch.
	if _, err := c.get(time.Minute, base.Add(time.Second), fetch); err != nil {
		t.Fatalf("post-invalidate get: %v", err)
	}
	if calls != 2 {
		t.Fatalf("fetch called %d times after invalidate, want 2", calls)
	}
}

func TestCachedSlice_TTLZeroDisablesCaching(t *testing.T) {
	t.Parallel()

	var calls int
	fetch := func() ([]int, error) {
		calls++
		return []int{calls}, nil
	}

	var c cachedSlice[int]
	base := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if _, err := c.get(0, base, fetch); err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	if calls != 3 {
		t.Fatalf("fetch called %d times with ttl=0, want 3 (caching disabled)", calls)
	}
}

func TestCachedSlice_FetchErrorNotCached(t *testing.T) {
	t.Parallel()

	var calls int
	wantErr := errors.New("boom")
	fetch := func() ([]int, error) {
		calls++
		if calls == 1 {
			return nil, wantErr
		}
		return []int{calls}, nil
	}

	var c cachedSlice[int]
	base := time.Unix(1000, 0)

	if _, err := c.get(time.Minute, base, fetch); !errors.Is(err, wantErr) {
		t.Fatalf("first get err = %v, want %v", err, wantErr)
	}
	// The error must not be cached — a subsequent get retries.
	got, err := c.get(time.Minute, base.Add(time.Second), fetch)
	if err != nil {
		t.Fatalf("retry get: %v", err)
	}
	if calls != 2 || got[0] != 2 {
		t.Fatalf("calls=%d got=%v, want retry after a non-cached error", calls, got)
	}
}
