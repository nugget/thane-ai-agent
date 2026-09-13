package email

import (
	"sync"
	"time"
)

// defaultOpLogSize is the number of recent operations retained for the
// Email Accounts context block.
const defaultOpLogSize = 20

// Operation records one successful email tool invocation: enough for
// the next turn to see what the mailbox was just asked to do without
// replaying the conversation.
type Operation struct {
	// Tool is the tool name, for example "email_move".
	Tool string `json:"tool"`

	// Account is the account the operation ran against.
	Account string `json:"account"`

	// Folder is the folder involved, when the operation had one.
	Folder string `json:"folder,omitempty"`

	// Ref is a compact reference: a UID, a UID range, a Message-ID, or
	// a destination folder.
	Ref string `json:"ref,omitempty"`

	// Timestamp is when the operation completed. It renders as a delta.
	Timestamp time.Time `json:"-"`
}

// OperationLog is a bounded ring of recent successful operations, safe
// for concurrent use. Only successes are recorded: a failed call is
// reported to the model as an error in the turn that made it, and
// repeating it here would double-count what the model already knows.
type OperationLog struct {
	mu      sync.Mutex
	entries []Operation
	head    int
	count   int
}

// NewOperationLog creates an empty log with the default capacity.
func NewOperationLog() *OperationLog {
	return &OperationLog{entries: make([]Operation, defaultOpLogSize)}
}

// Record appends an operation, stamping its timestamp inside the lock
// so insertion order and time order agree.
func (l *OperationLog) Record(op Operation) {
	if l == nil {
		return
	}
	l.mu.Lock()
	op.Timestamp = time.Now()
	l.entries[l.head] = op
	l.head = (l.head + 1) % len(l.entries)
	if l.count < len(l.entries) {
		l.count++
	}
	l.mu.Unlock()
}

// Recent returns the newest n operations, newest first, or fewer when
// the log holds fewer. It returns nil when the log is empty.
func (l *OperationLog) Recent(n int) []Operation {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count == 0 || n <= 0 {
		return nil
	}
	n = min(n, l.count)
	out := make([]Operation, n)
	size := len(l.entries)
	for i := 0; i < n; i++ {
		out[i] = l.entries[(l.head-1-i+size)%size]
	}
	return out
}

// Len returns the number of operations stored.
func (l *OperationLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}
