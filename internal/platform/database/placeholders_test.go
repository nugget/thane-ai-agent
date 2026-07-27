package database

import "testing"

func TestPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{-1, ""},
		{0, ""},
		{1, "?"},
		{2, "?,?"},
		{5, "?,?,?,?,?"},
	} {
		if got := Placeholders(tc.n); got != tc.want {
			t.Errorf("Placeholders(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestPlaceholders_LargeBatchIsWellFormed guards the shape at the sizes
// callers actually reach — the archiver deletes 250 IDs at a time — where
// an off-by-one in the trim would be invisible in a two-element test.
func TestPlaceholders_LargeBatchIsWellFormed(t *testing.T) {
	got := Placeholders(250)
	if n := len(got); n != 250*2-1 {
		t.Fatalf("length = %d, want %d", n, 250*2-1)
	}
	if got[0] != '?' || got[len(got)-1] != '?' {
		t.Errorf("must not start or end with a separator: %q...%q", got[:3], got[len(got)-3:])
	}
}

func TestInList(t *testing.T) {
	clause, args := InList([]string{"a", "b", "c"})
	if clause != "?,?,?" {
		t.Errorf("clause = %q, want %q", clause, "?,?,?")
	}
	if len(args) != 3 || args[0] != "a" || args[2] != "c" {
		t.Errorf("args = %v, want [a b c]", args)
	}

	// Non-string element types are the reason this is generic.
	clause, args = InList([]int{7, 8})
	if clause != "?,?" || len(args) != 2 || args[1] != 8 {
		t.Errorf("InList([]int) = %q, %v", clause, args)
	}

	// Empty must yield an empty clause so callers can detect "no query
	// to run" — interpolating it would produce the invalid `IN ()`.
	clause, args = InList([]string{})
	if clause != "" || args != nil {
		t.Errorf("empty slice = %q, %v; want \"\", nil", clause, args)
	}
	if clause, args = InList[string](nil); clause != "" || args != nil {
		t.Errorf("nil slice = %q, %v; want \"\", nil", clause, args)
	}
}
