package forge

import (
	"sync"
	"testing"
	"time"
)

func TestOperationLog_Record(t *testing.T) {
	l := NewOperationLog()

	l.Record(Operation{Tool: "forge_pr_get", Account: "github", Repo: "owner/repo", Ref: "#42"})

	if l.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", l.Len())
	}

	ops := l.Recent(10)
	if len(ops) != 1 {
		t.Fatalf("Recent(10) = %d ops, want 1", len(ops))
	}
	if ops[0].Tool != "forge_pr_get" {
		t.Errorf("Tool = %q, want forge_pr_get", ops[0].Tool)
	}
	if ops[0].Ref != "#42" {
		t.Errorf("Ref = %q, want #42", ops[0].Ref)
	}
	if ops[0].Timestamp.IsZero() {
		t.Error("Timestamp should be set automatically")
	}
}

func TestOperationLog_NewestFirst(t *testing.T) {
	l := NewOperationLog()

	l.Record(Operation{Tool: "first"})
	time.Sleep(time.Millisecond)
	l.Record(Operation{Tool: "second"})
	time.Sleep(time.Millisecond)
	l.Record(Operation{Tool: "third"})

	ops := l.Recent(3)
	if ops[0].Tool != "third" {
		t.Errorf("most recent should be 'third', got %q", ops[0].Tool)
	}
	if ops[2].Tool != "first" {
		t.Errorf("oldest should be 'first', got %q", ops[2].Tool)
	}
}

func TestOperationLog_RecentLimit(t *testing.T) {
	l := NewOperationLog()

	for i := 0; i < 5; i++ {
		l.Record(Operation{Tool: "op"})
	}

	ops := l.Recent(3)
	if len(ops) != 3 {
		t.Errorf("Recent(3) with 5 entries should return 3, got %d", len(ops))
	}

	ops = l.Recent(10)
	if len(ops) != 5 {
		t.Errorf("Recent(10) with 5 entries should return 5, got %d", len(ops))
	}
}

func TestOperationLog_CircularEviction(t *testing.T) {
	l := &OperationLog{
		entries: make([]Operation, 3), // capacity of 3
	}

	l.Record(Operation{Tool: "a"})
	l.Record(Operation{Tool: "b"})
	l.Record(Operation{Tool: "c"})
	l.Record(Operation{Tool: "d"}) // evicts "a"
	l.Record(Operation{Tool: "e"}) // evicts "b"

	ops := l.Recent(10)
	if len(ops) != 3 {
		t.Fatalf("expected 3 entries after eviction, got %d", len(ops))
	}
	// Newest first: e, d, c
	if ops[0].Tool != "e" || ops[1].Tool != "d" || ops[2].Tool != "c" {
		t.Errorf("expected [e, d, c], got [%s, %s, %s]", ops[0].Tool, ops[1].Tool, ops[2].Tool)
	}
}

func TestOperationLog_Empty(t *testing.T) {
	l := NewOperationLog()

	if l.Len() != 0 {
		t.Errorf("empty log Len() = %d, want 0", l.Len())
	}
	ops := l.Recent(5)
	if ops != nil {
		t.Errorf("empty log Recent() should return nil, got %v", ops)
	}
}

func TestOperationLog_Concurrent(t *testing.T) {
	l := NewOperationLog()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			l.Record(Operation{Tool: "concurrent_write"})
		}()
		go func() {
			defer wg.Done()
			_ = l.Recent(5)
		}()
	}
	wg.Wait()

	if l.Len() == 0 {
		t.Error("expected entries after concurrent writes")
	}
}
