package loop

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestShouldRunSupervisor pins the three-way contract of the
// post-PR-F2 supervisor decision: forced beats random beats none.
// Tests the predicate directly so it can be exercised without
// driving a full loop.
func TestShouldRunSupervisor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		supervisor     bool
		supervisorProb float64
		randValue      float64
		forced         bool
		wantBool       bool
		wantTrigger    SupervisorTrigger
	}{
		{
			name:        "forced wins over disabled supervisor",
			supervisor:  false,
			forced:      true,
			wantBool:    true,
			wantTrigger: SupervisorTriggerForced,
		},
		{
			name:           "forced wins over random",
			supervisor:     true,
			supervisorProb: 1.0,
			randValue:      0.5,
			forced:         true,
			wantBool:       true,
			wantTrigger:    SupervisorTriggerForced,
		},
		{
			name:           "random fires when supervisor enabled and dice come up",
			supervisor:     true,
			supervisorProb: 0.5,
			randValue:      0.4,
			forced:         false,
			wantBool:       true,
			wantTrigger:    SupervisorTriggerRandom,
		},
		{
			name:           "random does not fire when dice exceed prob",
			supervisor:     true,
			supervisorProb: 0.5,
			randValue:      0.6,
			forced:         false,
			wantBool:       false,
			wantTrigger:    SupervisorTriggerNone,
		},
		{
			name:           "random does not fire when supervisor disabled",
			supervisor:     false,
			supervisorProb: 1.0,
			randValue:      0.0,
			forced:         false,
			wantBool:       false,
			wantTrigger:    SupervisorTriggerNone,
		},
		{
			name:           "random does not fire when prob is zero",
			supervisor:     true,
			supervisorProb: 0.0,
			randValue:      0.0,
			forced:         false,
			wantBool:       false,
			wantTrigger:    SupervisorTriggerNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &Loop{
				config: Config{
					Supervisor:     tc.supervisor,
					SupervisorProb: tc.supervisorProb,
				},
				deps: Deps{Rand: fixedRand{tc.randValue}},
			}
			gotBool, gotTrigger := l.shouldRunSupervisor(tc.forced)
			if gotBool != tc.wantBool {
				t.Errorf("isSupervisor = %v, want %v", gotBool, tc.wantBool)
			}
			if gotTrigger != tc.wantTrigger {
				t.Errorf("trigger = %q, want %q", gotTrigger, tc.wantTrigger)
			}
		})
	}
}

// TestSupervisorTriggerStampedOnIterationRecord exercises the
// end-to-end plumbing: a loop with SupervisorProb=1.0 should
// produce iteration records carrying SupervisorTrigger="random",
// and the loop's Status() should report the same value on
// LastSupervisorTrigger. Without this, retrospection on
// supervisor cadence would see "this iteration was a supervisor
// turn" but lose the cause.
func TestSupervisorTriggerStampedOnIterationRecord(t *testing.T) {
	t.Parallel()

	var iterSnapshots atomic.Int32
	var lastTrigger atomic.Value // SupervisorTrigger
	runner := &inspectingRunner{
		onRun: func(req RunRequest) {
			iterSnapshots.Add(1)
		},
	}
	l, err := New(Config{
		Name:           "trigger-test",
		Task:           "t",
		SleepMin:       time.Millisecond,
		SleepMax:       2 * time.Millisecond,
		SleepDefault:   time.Millisecond,
		Jitter:         Float64Ptr(0),
		MaxIter:        2,
		Supervisor:     true,
		SupervisorProb: 1.0, // every iteration is supervisor
	}, Deps{Runner: runner, Rand: fixedRand{0.5}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = l.Start(context.Background())
	<-l.Done()

	status := l.Status()
	if status.LastSupervisorIter == 0 {
		t.Fatal("LastSupervisorIter is zero — no supervisor turn ran")
	}
	if status.LastSupervisorTrigger != SupervisorTriggerRandom {
		t.Errorf("LastSupervisorTrigger = %q, want %q (Bernoulli at prob=1.0)",
			status.LastSupervisorTrigger, SupervisorTriggerRandom)
	}
	if len(status.RecentIterations) == 0 {
		t.Fatal("no iteration snapshots recorded")
	}
	for i, snap := range status.RecentIterations {
		if !snap.Supervisor {
			t.Errorf("snap[%d].Supervisor = false; want true (prob=1.0)", i)
		}
		if snap.SupervisorTrigger != SupervisorTriggerRandom {
			t.Errorf("snap[%d].SupervisorTrigger = %q, want %q",
				i, snap.SupervisorTrigger, SupervisorTriggerRandom)
			lastTrigger.Store(snap.SupervisorTrigger)
		}
	}
}

// TestSupervisorTriggerForcedFromNotification covers the
// alternate plumbing: a notification with force_supervisor=true
// must produce SupervisorTrigger="forced" on the iteration
// record. We can't easily drive a real notification through
// here, so we exercise shouldRunSupervisor with forced=true to
// validate the contract — the upstream caller's
// consumePendingNotifies path is tested elsewhere.
func TestSupervisorTriggerForcedFromNotification(t *testing.T) {
	t.Parallel()

	// Construct a loop whose normal random path would also pick
	// supervisor (prob=1.0). Forced should still produce
	// "forced", not "random" — the typed cause distinguishes
	// the two even when both are "true."
	l := &Loop{
		config: Config{
			Supervisor:     true,
			SupervisorProb: 1.0,
		},
		deps: Deps{Rand: fixedRand{0.0}},
	}
	isSup, trigger := l.shouldRunSupervisor(true)
	if !isSup {
		t.Fatal("forced=true didn't produce supervisor turn")
	}
	if trigger != SupervisorTriggerForced {
		t.Errorf("trigger = %q, want %q (forced beats random)", trigger, SupervisorTriggerForced)
	}
}
