package checkout

import "testing"

func TestSyncStateRegistry(t *testing.T) {
	r := NewSyncStateRegistry()

	if got := r.LastKnownRemote("kb"); got != "" {
		t.Errorf("LastKnownRemote on empty = %q, want empty", got)
	}
	if _, ok := r.Get("kb"); ok {
		t.Error("Get on empty registry returned ok")
	}

	r.RecordState(SyncState{Name: "kb", OK: true, Outcome: SyncClean})
	r.RecordState(SyncState{Name: "apex", OK: true, Outcome: SyncFastForwarded})
	if st, ok := r.Get("kb"); !ok || st.Outcome != SyncClean {
		t.Errorf("Get(kb) = %+v, ok=%v", st, ok)
	}

	all := r.All()
	if len(all) != 2 || all[0].Name != "apex" || all[1].Name != "kb" {
		t.Fatalf("All() not sorted by name: %+v", all)
	}

	r.AdvanceRemote("kb", "sha1")
	if got := r.LastKnownRemote("kb"); got != "sha1" {
		t.Errorf("LastKnownRemote(kb) = %q, want sha1", got)
	}
	r.AdvanceRemote("kb", "") // no-op
	if got := r.LastKnownRemote("kb"); got != "sha1" {
		t.Errorf("AdvanceRemote with empty sha changed value to %q", got)
	}
}

func TestSyncStateRegistryZeroValue(t *testing.T) {
	var r SyncStateRegistry

	if prev, ok := r.RecordState(SyncState{Name: "kb", OK: true, Outcome: SyncClean}); ok {
		t.Fatalf("RecordState on zero value replaced %+v, want no previous state", prev)
	}
	if st, ok := r.Get("kb"); !ok || st.Outcome != SyncClean {
		t.Fatalf("Get(kb) after zero-value RecordState = %+v, ok=%v", st, ok)
	}

	r.AdvanceRemote("kb", "sha1")
	if got := r.LastKnownRemote("kb"); got != "sha1" {
		t.Fatalf("LastKnownRemote(kb) after zero-value AdvanceRemote = %q, want sha1", got)
	}
}

// TestSyncStateRegistryConcurrent exercises a syncer loop racing an
// operability surface under -race.
func TestSyncStateRegistryConcurrent(t *testing.T) {
	r := NewSyncStateRegistry()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			r.RecordState(SyncState{Name: "kb", Outcome: SyncClean})
			r.AdvanceRemote("kb", "sha")
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = r.All()
		_, _ = r.Get("kb")
		_ = r.LastKnownRemote("kb")
	}
	<-done
}
