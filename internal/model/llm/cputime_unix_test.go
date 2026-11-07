//go:build darwin || linux

package llm

import (
	"syscall"
	"testing"
	"time"
)

// processCPUTime returns the user and system CPU time the test process
// has used so far. Time spent waiting for a core is not counted, so a
// ratio of two such measurements is not inflated when the machine is
// oversubscribed, as a shared CI runner often is.
func processCPUTime(t testing.TB) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
