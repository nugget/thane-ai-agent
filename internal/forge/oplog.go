package forge

import (
	"sync"
	"time"
)

// defaultOpLogSize is the maximum number of operations retained.
const defaultOpLogSize = 20

// Operation records a single forge tool invocation.
type Operation struct {
	Tool      string    `json:"tool"`
	Account   string    `json:"account"`
	Repo      string    `json:"repo"`
	Ref       string    `json:"ref,omitempty"` // issue/PR number, search query, etc.
	Timestamp time.Time `json:"-"`             // formatted as delta at output time
}

// OperationLog tracks recent forge tool invocations in a bounded
// circular buffer. It is safe for concurrent use. Operations are
// recorded after successful tool execution and included in the forge
// capability context so delegates and subsequent turns know what's
// been happening.
type OperationLog struct {
	mu      sync.Mutex
	entries []Operation
	head    int
	count   int
}

// NewOperationLog creates an operation log with the default capacity.
func NewOperationLog() *OperationLog {
	return &OperationLog{
		entries: make([]Operation, defaultOpLogSize),
	}
}

// Record adds a successful operation to the log. The timestamp is
// set automatically. Failed operations should not be recorded.
func (l *OperationLog) Record(op Operation) {
	l.mu.Lock()
	op.Timestamp = time.Now() // inside lock so ordering matches insertion order
	l.entries[l.head] = op
	l.head = (l.head + 1) % len(l.entries)
	if l.count < len(l.entries) {
		l.count++
	}
	l.mu.Unlock()
}

// Recent returns the last n operations in reverse chronological order
// (newest first). Returns fewer than n if the log has fewer entries.
func (l *OperationLog) Recent(n int) []Operation {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.count == 0 {
		return nil
	}
	if n > l.count {
		n = l.count
	}

	result := make([]Operation, n)
	bufLen := len(l.entries)
	for i := 0; i < n; i++ {
		idx := (l.head - 1 - i + bufLen) % bufLen
		result[i] = l.entries[idx]
	}
	return result
}

// Len returns the number of operations currently stored.
func (l *OperationLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}
