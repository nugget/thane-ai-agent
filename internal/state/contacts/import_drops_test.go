package contacts

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestImportDrops_NamesSkippedCards pins that an import result names
// each skipped card with its cause and remedy, and bounds the list.
func TestImportDrops_NamesSkippedCards(t *testing.T) {
	many := make([]int, importCardListMax+5)
	for i := range many {
		many[i] = i + 1
	}
	tests := []struct {
		name  string
		drops importDrops
		want  []string
		avoid []string
	}{
		{
			name:  "nameless card",
			drops: importDrops{nameless: []int{2}},
			want:  []string{"1 card(s) were skipped because they have no usable name (FN): card 2.", "Give each a plain FN"},
		},
		{
			name:  "re-zoned merge target",
			drops: importDrops{changed: []int{3, 7}},
			want:  []string{"2 card(s) were skipped, not merged", "re-zoned or deleted", "cards 3, 7.", "with merge on"},
		},
		{
			name:  "failed write",
			drops: importDrops{unwritten: []int{4}},
			want:  []string{"1 card(s) were skipped because their write failed", "card 4.", "the log names each error", "with merge on"},
		},
		{
			name:  "list stops at the cap and counts the rest",
			drops: importDrops{unwritten: many},
			want:  []string{"25 card(s)", "cards 1, 2, 3,", ", 20 and 5 more."},
			avoid: []string{"21"},
		},
		{
			name:  "nothing skipped",
			drops: importDrops{},
			avoid: []string{"skipped"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.drops.notes()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("notes() = %q, want %q", got, w)
				}
			}
			for _, a := range tt.avoid {
				if strings.Contains(got, a) {
					t.Errorf("notes() = %q, want no %q", got, a)
				}
			}
		})
	}
}

// TestImportVCF_FailedWriteNamesTheCard pins that a card whose write
// transaction fails writes nothing and is named as skipped with the
// failed-write cause, while the cards before it keep their writes.
func TestImportVCF_FailedWriteNamesTheCard(t *testing.T) {
	tools := newTestTools(t)
	seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "ada@example.com"})
	// Card 2's refusal lands after card 1 is written and before card
	// 2's write transaction; the trigger stands in for an operator write
	// that collides with that transaction.
	onImportRefusal(t, func() {
		if _, err := tools.store.db.Exec(`CREATE TRIGGER import_collision BEFORE INSERT ON contacts BEGIN SELECT RAISE(ABORT, 'collision'); END`); err != nil {
			t.Errorf("create trigger: %v", err)
		}
	})

	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob One\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob Two\r\nEMAIL:ada@example.com\r\nTEL:+15550007777\r\nEND:VCARD\r\n"
	out := importText(t, tools, vcf, map[string]any{"merge": false})
	for _, want := range []string{"1 created, 0 merged, 1 skipped", "1 card(s) were skipped because their write failed", "card 2."} {
		if !strings.Contains(out, want) {
			t.Errorf("import = %q, want %q", out, want)
		}
	}
	if _, err := tools.store.FindByName("Bob One"); err != nil {
		t.Errorf("card 1 was not written: %v", err)
	}
	if _, err := tools.store.FindByName("Bob Two"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the failed card was written: %v", err)
	}
}
