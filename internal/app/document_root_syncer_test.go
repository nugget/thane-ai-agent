package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseSyncInterval(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"", defaultSyncInterval, false},
		{"   ", defaultSyncInterval, false}, // whitespace-only trims to empty
		{"0", 0, false},
		{"30s", 30 * time.Second, false},
		{"60s ", 60 * time.Second, false}, // trailing space (validation trims)
		{"5m", 5 * time.Minute, false},
		{"nonsense", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSyncInterval(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSyncInterval(%q) = nil error, want error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSyncInterval(%q): %v", tt.raw, err)
		}
		if got != tt.want {
			t.Errorf("parseSyncInterval(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestBuildSyncRequest(t *testing.T) {
	identity := func(s string) string { return s }

	t.Run("bidirectional ssh required", func(t *testing.T) {
		req := buildSyncRequest(config.DocumentRootGitConfig{
			VerifySignatures: "required",
			Remote: &config.DocumentRootGitRemoteConfig{
				URL:  "git@example.com:o/r.git",
				Mode: "bidirectional",
				Auth: config.DocumentRootGitRemoteAuthConfig{SSHKey: "/k", KnownHosts: "/kh"},
			},
		}, identity)
		if req.RemoteURL != "git@example.com:o/r.git" {
			t.Errorf("RemoteURL = %q", req.RemoteURL)
		}
		if req.Branch != "main" {
			t.Errorf("Branch = %q, want default main", req.Branch)
		}
		if req.Mode != checkout.SyncModeBidirectional {
			t.Errorf("Mode = %v, want bidirectional", req.Mode)
		}
		if !req.RequireVerify {
			t.Error("RequireVerify = false, want true for verify_signatures=required")
		}
		if req.SSHCommand == "" {
			t.Error("SSHCommand empty, want a built ssh command for ssh auth")
		}
	})

	t.Run("fetch-only https warn", func(t *testing.T) {
		req := buildSyncRequest(config.DocumentRootGitConfig{
			VerifySignatures: "warn",
			Remote: &config.DocumentRootGitRemoteConfig{
				URL:    "https://example.com/r.git",
				Branch: "trunk",
				Mode:   "fetch",
			},
		}, identity)
		if req.Branch != "trunk" {
			t.Errorf("Branch = %q, want trunk", req.Branch)
		}
		if req.Mode != checkout.SyncModeFetch {
			t.Errorf("Mode = %v, want fetch", req.Mode)
		}
		if !req.RequireVerify {
			t.Error("RequireVerify = false, want true for verify_signatures=warn")
		}
		if req.SSHCommand != "" {
			t.Errorf("SSHCommand = %q, want empty for https with no ssh auth", req.SSHCommand)
		}
	})

	t.Run("verify none", func(t *testing.T) {
		req := buildSyncRequest(config.DocumentRootGitConfig{
			VerifySignatures: "none",
			Remote:           &config.DocumentRootGitRemoteConfig{URL: "u", Mode: "fetch"},
		}, identity)
		if req.RequireVerify {
			t.Error("RequireVerify = true, want false for verify_signatures=none")
		}
	})

	t.Run("trimmed verify and mode fail closed", func(t *testing.T) {
		// A quoted trailing space passes config validation (which trims); the
		// consumer must trim too, or it fails open (no verify / no push).
		req := buildSyncRequest(config.DocumentRootGitConfig{
			VerifySignatures: "required ",
			Remote:           &config.DocumentRootGitRemoteConfig{URL: "u", Mode: "bidirectional "},
		}, identity)
		if !req.RequireVerify {
			t.Error("RequireVerify = false for 'required ' (trailing space); must fail closed to true")
		}
		if req.Mode != checkout.SyncModeBidirectional {
			t.Error("Mode != bidirectional for 'bidirectional ' (trailing space)")
		}
	})

	t.Run("trimmed url branch and ssh paths", func(t *testing.T) {
		req := buildSyncRequest(config.DocumentRootGitConfig{
			VerifySignatures: "required",
			Remote: &config.DocumentRootGitRemoteConfig{
				URL:    " git@example.com:o/r.git ",
				Branch: " trunk ",
				Mode:   "bidirectional",
				Auth:   config.DocumentRootGitRemoteAuthConfig{SSHKey: " /k ", KnownHosts: " /kh "},
			},
		}, identity)
		if req.RemoteURL != "git@example.com:o/r.git" {
			t.Errorf("RemoteURL = %q, want trimmed", req.RemoteURL)
		}
		if req.Branch != "trunk" {
			t.Errorf("Branch = %q, want trimmed trunk", req.Branch)
		}
		// The built ssh command must reference the trimmed key/known_hosts,
		// never the padded values.
		if !strings.Contains(req.SSHCommand, "'/k'") || !strings.Contains(req.SSHCommand, "'/kh'") {
			t.Errorf("SSHCommand = %q, want trimmed key/known_hosts", req.SSHCommand)
		}
	})
}

func TestBuildDocRootSyncer(t *testing.T) {
	reg := newSyncStateRegistry()
	identity := func(s string) string { return s }

	t.Run("nil remote yields nil syncer", func(t *testing.T) {
		s, err := buildDocRootSyncer("kb", config.DocumentRootGitConfig{}, &fakeEngine{}, reg, identity, testLogger())
		if err != nil || s != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", s, err)
		}
	})

	t.Run("trust_anchor is deferred with a clear error", func(t *testing.T) {
		_, err := buildDocRootSyncer("kb", config.DocumentRootGitConfig{
			Remote: &config.DocumentRootGitRemoteConfig{URL: "u", Mode: "fetch", TrustAnchor: "/anchor"},
		}, &fakeEngine{}, reg, identity, testLogger())
		if err == nil {
			t.Fatal("trust_anchor accepted, want a not-yet-wired error")
		}
	})

	t.Run("bad interval errors", func(t *testing.T) {
		_, err := buildDocRootSyncer("kb", config.DocumentRootGitConfig{
			Remote: &config.DocumentRootGitRemoteConfig{URL: "u", Mode: "fetch", Interval: "xyz"},
		}, &fakeEngine{}, reg, identity, testLogger())
		if err == nil {
			t.Fatal("bad interval accepted")
		}
	})

	t.Run("constructs with mapped request and interval", func(t *testing.T) {
		s, err := buildDocRootSyncer("kb", config.DocumentRootGitConfig{
			VerifySignatures: "required",
			Remote:           &config.DocumentRootGitRemoteConfig{URL: "u", Mode: "bidirectional", Interval: "30s"},
		}, &fakeEngine{}, reg, identity, testLogger())
		if err != nil {
			t.Fatalf("buildDocRootSyncer: %v", err)
		}
		if s.root != "kb" || s.interval != 30*time.Second {
			t.Errorf("root=%q interval=%v, want kb/30s", s.root, s.interval)
		}
		if s.request.Mode != checkout.SyncModeBidirectional || !s.request.RequireVerify {
			t.Errorf("request = %+v, want bidirectional + RequireVerify", s.request)
		}
		if s.registry != reg {
			t.Error("registry not wired")
		}
	})
}

func TestSyncStateRegistry(t *testing.T) {
	r := newSyncStateRegistry()

	if got := r.lastKnownRemote("kb"); got != "" {
		t.Errorf("lastKnownRemote on empty = %q, want empty", got)
	}
	if _, ok := r.get("kb"); ok {
		t.Error("get on empty registry returned ok")
	}

	r.setState(syncState{Root: "kb", OK: true, Outcome: checkout.SyncClean})
	r.setState(syncState{Root: "apex", OK: true, Outcome: checkout.SyncFastForwarded})
	if st, ok := r.get("kb"); !ok || st.Outcome != checkout.SyncClean {
		t.Errorf("get(kb) = %+v, ok=%v", st, ok)
	}

	all := r.all()
	if len(all) != 2 || all[0].Root != "apex" || all[1].Root != "kb" {
		t.Fatalf("all() not sorted by root: %+v", all)
	}

	r.advanceRemote("kb", "sha1")
	if got := r.lastKnownRemote("kb"); got != "sha1" {
		t.Errorf("lastKnownRemote(kb) = %q, want sha1", got)
	}
	r.advanceRemote("kb", "") // no-op
	if got := r.lastKnownRemote("kb"); got != "sha1" {
		t.Errorf("advanceRemote with empty sha changed value to %q", got)
	}
}

// TestSyncStateRegistryConcurrent exercises the writer (syncer loop) racing the
// reader (operability surface) under -race.
func TestSyncStateRegistryConcurrent(t *testing.T) {
	r := newSyncStateRegistry()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			r.setState(syncState{Root: "kb", Outcome: checkout.SyncClean})
			r.advanceRemote("kb", "sha")
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = r.all()
		_, _ = r.get("kb")
		_ = r.lastKnownRemote("kb")
	}
	<-done
}

// fakeEngine returns scripted results for successive Sync calls and records the
// requests it received.
type fakeEngine struct {
	results []checkout.SyncResult
	errs    []error
	calls   []checkout.SyncRequest
}

func (f *fakeEngine) Sync(_ context.Context, req checkout.SyncRequest) (checkout.SyncResult, error) {
	i := len(f.calls)
	f.calls = append(f.calls, req)
	var res checkout.SyncResult
	var err error
	if i < len(f.results) {
		res = f.results[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return res, err
}

func newTestSyncer(engine syncEngine, reg *syncStateRegistry, refresh func(context.Context) error) *docRootSyncer {
	return &docRootSyncer{
		root:     "kb",
		engine:   engine,
		request:  checkout.SyncRequest{Branch: "main", Mode: checkout.SyncModeBidirectional, RequireVerify: true},
		refresh:  refresh,
		registry: reg,
		logger:   testLogger(),
	}
}

func TestRunOnceFastForwardReindexesAndAdvances(t *testing.T) {
	reg := newSyncStateRegistry()
	refreshes := 0
	eng := &fakeEngine{results: []checkout.SyncResult{
		{Outcome: checkout.SyncFastForwarded, Behind: 2, LocalHead: "l", RemoteHead: "rA"},
		{Outcome: checkout.SyncClean, LocalHead: "rA", RemoteHead: "rA"},
	}}
	s := newTestSyncer(eng, reg, func(context.Context) error { refreshes++; return nil })

	st := s.runOnce(context.Background())
	if !st.OK || st.Outcome != checkout.SyncFastForwarded {
		t.Fatalf("state = %+v, want ok fast_forwarded", st)
	}
	if refreshes != 1 {
		t.Errorf("refresh called %d times after fast-forward, want 1", refreshes)
	}
	if got := reg.lastKnownRemote("kb"); got != "rA" {
		t.Errorf("lastKnownRemote = %q, want rA", got)
	}

	// Second pass must thread the recorded remote head as LastKnownRemote.
	s.runOnce(context.Background())
	if len(eng.calls) != 2 {
		t.Fatalf("engine called %d times, want 2", len(eng.calls))
	}
	if eng.calls[1].LastKnownRemote != "rA" {
		t.Errorf("pass 2 LastKnownRemote = %q, want rA", eng.calls[1].LastKnownRemote)
	}
	if refreshes != 1 {
		t.Errorf("refresh called %d times, want still 1 (clean pass does not re-index)", refreshes)
	}
}

func TestRunOnceRemoteBehindDoesNotAdvanceOrReindex(t *testing.T) {
	reg := newSyncStateRegistry()
	reg.advanceRemote("kb", "rA") // a prior legitimate remote head
	refreshes := 0
	eng := &fakeEngine{results: []checkout.SyncResult{
		{Outcome: checkout.SyncRemoteBehind, LocalHead: "l", RemoteHead: "rOld", Detail: "rewound"},
	}}
	s := newTestSyncer(eng, reg, func(context.Context) error { refreshes++; return nil })

	st := s.runOnce(context.Background())
	if st.Outcome != checkout.SyncRemoteBehind {
		t.Fatalf("outcome = %q, want remote_behind", st.Outcome)
	}
	if refreshes != 0 {
		t.Errorf("refresh called on remote_behind, want 0")
	}
	// The rewind baseline must NOT advance, so the rewind keeps being detected.
	if got := reg.lastKnownRemote("kb"); got != "rA" {
		t.Errorf("lastKnownRemote = %q, want unchanged rA", got)
	}
}

// TestRunOnceAdvanceBaseline pins the rewind-baseline invariant: accepted
// outcomes advance it; refused outcomes leave it untouched so a later rewind
// stays detectable.
func TestRunOnceAdvanceBaseline(t *testing.T) {
	accepted := []checkout.SyncOutcome{checkout.SyncClean, checkout.SyncFastForwarded, checkout.SyncPushed}
	refused := []checkout.SyncOutcome{checkout.SyncDiverged, checkout.SyncBlocked, checkout.SyncRemoteBehind}

	for _, oc := range accepted {
		t.Run("advances_on_"+string(oc), func(t *testing.T) {
			reg := newSyncStateRegistry()
			reg.advanceRemote("kb", "prior")
			eng := &fakeEngine{results: []checkout.SyncResult{{Outcome: oc, RemoteHead: "new"}}}
			newTestSyncer(eng, reg, nil).runOnce(context.Background())
			if got := reg.lastKnownRemote("kb"); got != "new" {
				t.Errorf("lastKnownRemote = %q, want advanced to new for accepted outcome %q", got, oc)
			}
		})
	}
	for _, oc := range refused {
		t.Run("holds_on_"+string(oc), func(t *testing.T) {
			reg := newSyncStateRegistry()
			reg.advanceRemote("kb", "prior")
			eng := &fakeEngine{results: []checkout.SyncResult{{Outcome: oc, RemoteHead: "new"}}}
			newTestSyncer(eng, reg, nil).runOnce(context.Background())
			if got := reg.lastKnownRemote("kb"); got != "prior" {
				t.Errorf("lastKnownRemote = %q, want held at prior for refused outcome %q", got, oc)
			}
		})
	}
}

func TestRunOnceNotifiesOnAttentionTransitions(t *testing.T) {
	reg := newSyncStateRegistry()
	eng := &fakeEngine{results: []checkout.SyncResult{
		{Outcome: checkout.SyncBlocked, RemoteHead: "r1", Detail: "first untrusted commit abc123"},
		{Outcome: checkout.SyncBlocked, RemoteHead: "r1", Detail: "first untrusted commit abc123"},
		{Outcome: checkout.SyncBlocked, RemoteHead: "r2", Detail: "first untrusted commit def456"},
		{Outcome: checkout.SyncDiverged, RemoteHead: "r3", Detail: "local and remote diverged"},
		{Outcome: checkout.SyncFastForwarded, RemoteHead: "r3"},
		{Outcome: checkout.SyncClean, RemoteHead: "r3"},
	}}
	s := newTestSyncer(eng, reg, nil)
	var transitions []syncStateTransition
	s.notifyTransition = func(_ context.Context, tr syncStateTransition) error {
		transitions = append(transitions, tr)
		return nil
	}

	for range eng.results {
		s.runOnce(context.Background())
	}

	if len(transitions) != 4 {
		t.Fatalf("transitions len = %d, want 4: %+v", len(transitions), transitions)
	}
	tests := []struct {
		i       int
		kind    syncTransitionKind
		outcome checkout.SyncOutcome
		detail  string
	}{
		{0, syncTransitionAttentionRequired, checkout.SyncBlocked, "first untrusted commit abc123"},
		{1, syncTransitionAttentionRequired, checkout.SyncBlocked, "first untrusted commit def456"},
		{2, syncTransitionAttentionRequired, checkout.SyncDiverged, "local and remote diverged"},
		{3, syncTransitionRecovered, checkout.SyncFastForwarded, ""},
	}
	for _, tt := range tests {
		tr := transitions[tt.i]
		if tr.Kind != tt.kind || tr.Current.Outcome != tt.outcome || tr.Current.Detail != tt.detail {
			t.Errorf("transition[%d] = kind=%q outcome=%q detail=%q, want %q/%q/%q",
				tt.i, tr.Kind, tr.Current.Outcome, tr.Current.Detail, tt.kind, tt.outcome, tt.detail)
		}
	}
	if transitions[3].Previous.Outcome != checkout.SyncDiverged {
		t.Fatalf("recovery previous outcome = %q, want diverged", transitions[3].Previous.Outcome)
	}
}

// TestRunReturnsOnCancel covers both loop shapes: a manual (interval<=0) syncer
// runs one pass then blocks on ctx, and a ticker syncer waits — both must
// return promptly when the context is cancelled, and neither may panic on a
// zero interval.
func TestRunReturnsOnCancel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{"manual", 0},
		{"ticker", time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newSyncStateRegistry()
			eng := &fakeEngine{results: []checkout.SyncResult{{Outcome: checkout.SyncClean, RemoteHead: "r"}}}
			s := newTestSyncer(eng, reg, nil)
			s.interval = tc.interval
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { s.Run(ctx); close(done) }()
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not return promptly after ctx cancel")
			}
			if len(eng.calls) < 1 {
				t.Error("Run did not perform the initial pass")
			}
		})
	}
}

func TestRunOnceErrorRecordsState(t *testing.T) {
	reg := newSyncStateRegistry()
	eng := &fakeEngine{errs: []error{context.DeadlineExceeded}}
	s := newTestSyncer(eng, reg, nil)

	st := s.runOnce(context.Background())
	if st.OK {
		t.Error("OK = true on an errored pass, want false")
	}
	if st.Detail == "" {
		t.Error("Detail empty on error, want the error message")
	}
	if got, ok := reg.get("kb"); !ok || got.OK {
		t.Errorf("registry state = %+v, ok=%v; want recorded not-OK", got, ok)
	}
}
