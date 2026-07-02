package checkout

import (
	"sort"
	"sync"
	"time"
)

// SyncState is the in-memory observed state of a checkout's remote sync. It is
// re-derived every pass and can be surfaced by whichever domain owns the
// checkout.
type SyncState struct {
	// Name is the caller-facing checkout identifier.
	Name string
	// OK is false when the last pass errored before producing a SyncResult.
	OK bool
	// Outcome classifies the last completed sync pass.
	Outcome SyncOutcome
	Ahead   int
	Behind  int
	// LocalHead and RemoteHead are the resolved local and remote commit SHAs
	// observed by the sync pass.
	LocalHead  string
	RemoteHead string
	// Detail carries the reason for a blocked/diverged/remote_behind outcome,
	// or the error message when OK is false.
	Detail string
	// LastSyncAt records when this state was observed.
	LastSyncAt time.Time
}

// SyncStateRegistry stores per-checkout sync state and the remote head
// accepted on the previous pass. It is safe for concurrent readers and
// writers.
type SyncStateRegistry struct {
	mu         sync.Mutex
	state      map[string]SyncState
	lastRemote map[string]string
}

// NewSyncStateRegistry creates an empty sync-state registry.
func NewSyncStateRegistry() *SyncStateRegistry {
	return &SyncStateRegistry{
		state:      make(map[string]SyncState),
		lastRemote: make(map[string]string),
	}
}

// RecordState records the latest observed state for a checkout and returns the
// state it replaced, if any.
func (r *SyncStateRegistry) RecordState(st SyncState) (SyncState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLocked()
	prev, ok := r.state[st.Name]
	r.state[st.Name] = st
	return prev, ok
}

// AdvanceRemote records the remote head a checkout legitimately accepted, so
// the next pass can detect a rewind past it. Empty SHAs are ignored.
func (r *SyncStateRegistry) AdvanceRemote(name, sha string) {
	if sha == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLocked()
	r.lastRemote[name] = sha
}

// LastKnownRemote returns the remote head recorded for a checkout's previous
// accepted pass, or "" if none.
func (r *SyncStateRegistry) LastKnownRemote(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRemote[name]
}

// Get returns the recorded state for a checkout.
func (r *SyncStateRegistry) Get(name string) (SyncState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.state[name]
	return st, ok
}

// All returns every recorded state, sorted by Name for stable listings.
func (r *SyncStateRegistry) All() []SyncState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SyncState, 0, len(r.state))
	for _, st := range r.state {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *SyncStateRegistry) ensureLocked() {
	if r.state == nil {
		r.state = make(map[string]SyncState)
	}
	if r.lastRemote == nil {
		r.lastRemote = make(map[string]string)
	}
}
