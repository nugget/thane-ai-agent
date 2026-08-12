package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/app/coreloops"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestShippedCoreLoopDocumentsReferenceEverySpecKey pins the operator
// contract the shipped definition documents carry: their spec blocks
// double as the reference for the whole authorable surface, so every
// yaml key on [looppkg.Spec] must appear in each document — live, or as
// a commented reference line. The document loader refuses unknown keys,
// which makes the documents the only place an operator can see the
// accepted key names without reading Go; a Spec field that ships
// without a reference line is invisible exactly where an operator
// would look for it. Adding a field to Spec fails this test until the
// shipped documents mention it.
func TestShippedCoreLoopDocumentsReferenceEverySpecKey(t *testing.T) {
	typ := reflect.TypeOf(looppkg.Spec{})
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag == "" {
			t.Fatalf("Spec.%s has no yaml tag; authorable fields need one, runtime-only fields need yaml:\"-\"", field.Name)
		}
		keys = append(keys, tag)
	}

	entries, err := coreloops.Documents.ReadDir("defaults")
	if err != nil {
		t.Fatalf("read embedded defaults: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded core loop documents; run go generate ./internal/app/coreloops/")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := coreloops.Documents.ReadFile("defaults/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		doc := string(raw)
		for _, key := range keys {
			// task is not a spec-block key in these documents: the
			// "## Task" section carries the prompt, and declaring both
			// is refused by the loader. The section heading is what
			// satisfies the reference obligation — a reference line
			// documenting a key the same file forbids would only
			// confuse.
			if key == "task" {
				if !strings.Contains(doc, "## Task") {
					t.Errorf("%s: no \"## Task\" section; the task belongs there, not in the spec block", entry.Name())
				}
				continue
			}
			if !strings.Contains(doc, key+":") {
				t.Errorf("%s: spec key %q is neither set nor referenced; add it to the spec block's reference comment so operators can see the full surface", entry.Name(), key)
			}
		}
	}
}
