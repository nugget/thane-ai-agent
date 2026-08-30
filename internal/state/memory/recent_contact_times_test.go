package memory

import (
	"testing"
	"time"
)

// TestRecentContactTimes verifies contact classification over the unified
// messages table: stamped channel rows count in either direction, wake
// and internal rows never count, and the documented legacy fallback
// counts unstamped user rows except wake-bridge synthetic content.
func TestRecentContactTimes(t *testing.T) {
	store := newWindowStore(t, 100)
	archive, err := NewArchiveStoreFromDB(store.db, nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	insert := func(id, role, content string, origin any, ts time.Time) {
		t.Helper()
		if _, err := store.db.Exec(`
			INSERT INTO messages (id, conversation_id, role, content, timestamp, origin)
			VALUES (?, 'c1', ?, ?, ?, ?)
		`, id, role, content, ts, origin); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	t1 := base.Add(1 * time.Minute)
	t4 := base.Add(4 * time.Minute)
	t6 := base.Add(6 * time.Minute)

	insert("m1", "user", "inbound", OriginChannel, t1)
	insert("m2", "assistant", "loop reply", OriginInternal, base.Add(2*time.Minute))
	insert("m3", "user", "wake prompt", OriginWake, base.Add(3*time.Minute))
	insert("m4", "user", "legacy inbound", nil, t4) // NULL origin: pre-stamp row
	insert("m5", "user", "Anticipation matched: garage door", "", base.Add(5*time.Minute))
	insert("m6", "assistant", "outbound notification", OriginChannel, t6)
	insert("m7", "assistant", "legacy reply", "", base.Add(7*time.Minute))

	// Another conversation must not leak in.
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, origin)
		VALUES ('other', 'c2', 'user', 'elsewhere', ?, ?)
	`, base.Add(8*time.Minute), OriginChannel); err != nil {
		t.Fatalf("insert other-conversation row: %v", err)
	}

	got, err := archive.RecentContactTimes("c1", 5)
	if err != nil {
		t.Fatalf("RecentContactTimes: %v", err)
	}
	want := []time.Time{t6, t4, t1}
	if len(got) != len(want) {
		t.Fatalf("RecentContactTimes returned %d times, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("contact[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	limited, err := archive.RecentContactTimes("c1", 2)
	if err != nil {
		t.Fatalf("RecentContactTimes limited: %v", err)
	}
	if len(limited) != 2 || !limited[0].Equal(t6) || !limited[1].Equal(t4) {
		t.Errorf("limit 2 = %v, want [%v %v]", limited, t6, t4)
	}

	empty, err := archive.RecentContactTimes("c-nothing", 3)
	if err != nil {
		t.Fatalf("RecentContactTimes empty conversation: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty conversation returned %v, want none", empty)
	}
}
