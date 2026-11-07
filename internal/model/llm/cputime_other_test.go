//go:build !darwin && !linux

package llm

import (
	"testing"
	"time"
)

var processStart = time.Now()

// processCPUTime falls back to wall-clock time off darwin and linux,
// where getrusage is unavailable; a busy machine can then inflate a
// scaling ratio.
func processCPUTime(testing.TB) time.Duration {
	return time.Since(processStart)
}
