package mqtt

import (
	"sync"
	"testing"
	"time"
)

func TestDailyTokens_Record(t *testing.T) {
	dt := NewDailyTokens(time.UTC)
	dt.OnTokens(100, 200)
	dt.OnTokens(50, 75)

	input, output, requests := dt.Snapshot()
	if input != 150 {
		t.Errorf("input = %d, want 150", input)
	}
	if output != 275 {
		t.Errorf("output = %d, want 275", output)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestDailyTokens_Snapshot_ZeroInitially(t *testing.T) {
	dt := NewDailyTokens(time.UTC)
	input, output, requests := dt.Snapshot()
	if input != 0 || output != 0 || requests != 0 {
		t.Errorf("got (%d, %d, %d), want (0, 0, 0)", input, output, requests)
	}
}

func TestDailyTokens_Concurrent(t *testing.T) {
	dt := NewDailyTokens(time.UTC)
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dt.OnTokens(10, 20)
		}()
	}
	wg.Wait()

	input, output, requests := dt.Snapshot()
	if input != 1000 {
		t.Errorf("input = %d, want 1000", input)
	}
	if output != 2000 {
		t.Errorf("output = %d, want 2000", output)
	}
	if requests != 100 {
		t.Errorf("requests = %d, want 100", requests)
	}
}

func TestDailyTokens_MidnightReset(t *testing.T) {
	dt := NewDailyTokens(time.UTC)
	dt.OnTokens(500, 600)

	// Simulate date change by manipulating the resetDay field directly.
	dt.mu.Lock()
	dt.resetDay = time.Now().In(dt.loc).YearDay() - 1
	dt.mu.Unlock()

	// Next Snapshot should detect the day change and reset.
	input, output, requests := dt.Snapshot()
	if input != 0 {
		t.Errorf("input after reset = %d, want 0", input)
	}
	if output != 0 {
		t.Errorf("output after reset = %d, want 0", output)
	}
	if requests != 0 {
		t.Errorf("requests after reset = %d, want 0", requests)
	}
}

func TestDailyTokens_NilLocation(t *testing.T) {
	dt := NewDailyTokens(nil)
	if dt.loc != time.Local {
		t.Error("nil location should default to time.Local")
	}
	// Verify it works without panic.
	dt.OnTokens(1, 1)
	input, _, _ := dt.Snapshot()
	if input != 1 {
		t.Errorf("input = %d, want 1", input)
	}
}
