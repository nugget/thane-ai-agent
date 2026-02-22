package facts

import (
	"context"
	"log/slog"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSubjectsContext(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		subjects := []string{"entity:foo", "zone:bar"}
		ctx := WithSubjects(context.Background(), subjects)
		got := SubjectsFromContext(ctx)
		if len(got) != 2 || got[0] != "entity:foo" || got[1] != "zone:bar" {
			t.Errorf("SubjectsFromContext = %v, want %v", got, subjects)
		}
	})

	t.Run("nil subjects returns original context", func(t *testing.T) {
		ctx := WithSubjects(context.Background(), nil)
		got := SubjectsFromContext(ctx)
		if got != nil {
			t.Errorf("SubjectsFromContext = %v, want nil", got)
		}
	})

	t.Run("empty slice returns original context", func(t *testing.T) {
		ctx := WithSubjects(context.Background(), []string{})
		got := SubjectsFromContext(ctx)
		if got != nil {
			t.Errorf("SubjectsFromContext = %v, want nil", got)
		}
	})

	t.Run("missing key returns nil", func(t *testing.T) {
		got := SubjectsFromContext(context.Background())
		if got != nil {
			t.Errorf("SubjectsFromContext = %v, want nil", got)
		}
	})
}

func TestSubjectContextProvider_GetContext(t *testing.T) {
	tests := []struct {
		name         string
		subjects     []string   // subjects to inject into context
		facts        []testFact // facts to pre-populate
		maxFacts     int        // 0 = use default
		wantEmpty    bool
		wantContains []string
	}{
		{
			name:      "no subjects in context",
			subjects:  nil,
			wantEmpty: true,
		},
		{
			name:     "subjects with matching facts",
			subjects: []string{"entity:binary_sensor.driveway"},
			facts: []testFact{
				{
					category: CategoryDevice,
					key:      "driveway_camera_note",
					value:    "Camera captures FM 3040 road traffic",
					subjects: []string{"entity:binary_sensor.driveway", "camera:driveway_3040"},
				},
			},
			wantContains: []string{
				"driveway_camera_note",
				"Camera captures FM 3040 road traffic",
				"entity:binary_sensor.driveway",
			},
		},
		{
			name:     "subjects with no matching facts",
			subjects: []string{"entity:nonexistent"},
			facts: []testFact{
				{
					category: CategoryDevice,
					key:      "some_fact",
					value:    "some value",
					subjects: []string{"entity:other"},
				},
			},
			wantEmpty: true,
		},
		{
			name:     "multiple subjects match different facts",
			subjects: []string{"entity:sensor.temp", "zone:office"},
			facts: []testFact{
				{
					category: CategoryDevice,
					key:      "office_temp",
					value:    "Runs warm from equipment",
					subjects: []string{"entity:sensor.temp", "location:office"},
				},
				{
					category: CategoryHome,
					key:      "office_layout",
					value:    "Standing desk near window",
					subjects: []string{"zone:office"},
				},
			},
			wantContains: []string{
				"office_temp",
				"office_layout",
			},
		},
		{
			name:     "maxFacts limits output",
			subjects: []string{"zone:house"},
			facts: []testFact{
				{category: CategoryHome, key: "fact1", value: "v1", subjects: []string{"zone:house"}},
				{category: CategoryHome, key: "fact2", value: "v2", subjects: []string{"zone:house"}},
				{category: CategoryHome, key: "fact3", value: "v3", subjects: []string{"zone:house"}},
			},
			maxFacts:     2,
			wantContains: []string{"fact1", "fact2"},
		},
		{
			name:     "facts without subjects are not returned",
			subjects: []string{"entity:foo"},
			facts: []testFact{
				{category: CategoryDevice, key: "no_subjects", value: "general fact", subjects: nil},
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)

			// Populate facts.
			for _, f := range tt.facts {
				_, err := store.Set(f.category, f.key, f.value, "test", 1.0, f.subjects)
				if err != nil {
					t.Fatalf("Set(%s/%s): %v", f.category, f.key, err)
				}
			}

			provider := NewSubjectContextProvider(store, slog.Default())
			if tt.maxFacts > 0 {
				provider.SetMaxFacts(tt.maxFacts)
			}

			ctx := context.Background()
			if tt.subjects != nil {
				ctx = WithSubjects(ctx, tt.subjects)
			}

			got, err := provider.GetContext(ctx, "ignored")
			if err != nil {
				t.Fatalf("GetContext: %v", err)
			}

			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}

			if got == "" {
				t.Fatal("expected non-empty context")
			}

			for _, want := range tt.wantContains {
				if !containsString(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
		})
	}
}

type testFact struct {
	category Category
	key      string
	value    string
	subjects []string
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && searchString(haystack, needle)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGetBySubjects(t *testing.T) {
	store := newTestStore(t)

	// Create facts with different subjects.
	_, err := store.Set(CategoryDevice, "driveway_cam", "Captures road traffic", "test", 1.0,
		[]string{"entity:binary_sensor.driveway", "camera:driveway_3040"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Set(CategoryHome, "dan_info", "Lives in stable apartment", "test", 1.0,
		[]string{"contact:dan@example.com", "location:stable"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Set(CategoryPreference, "general_pref", "Prefers dark mode", "test", 1.0, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		subjects []string
		wantKeys []string
	}{
		{
			name:     "match single subject",
			subjects: []string{"entity:binary_sensor.driveway"},
			wantKeys: []string{"driveway_cam"},
		},
		{
			name:     "match by contact",
			subjects: []string{"contact:dan@example.com"},
			wantKeys: []string{"dan_info"},
		},
		{
			name:     "match multiple subjects across facts",
			subjects: []string{"entity:binary_sensor.driveway", "location:stable"},
			wantKeys: []string{"driveway_cam", "dan_info"},
		},
		{
			name:     "no match",
			subjects: []string{"entity:nonexistent"},
			wantKeys: nil,
		},
		{
			name:     "nil subjects",
			subjects: nil,
			wantKeys: nil,
		},
		{
			name:     "empty subjects",
			subjects: []string{},
			wantKeys: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts, err := store.GetBySubjects(tt.subjects)
			if err != nil {
				t.Fatalf("GetBySubjects: %v", err)
			}

			if len(facts) != len(tt.wantKeys) {
				t.Fatalf("got %d facts, want %d", len(facts), len(tt.wantKeys))
			}

			gotKeys := make(map[string]bool)
			for _, f := range facts {
				gotKeys[f.Key] = true
			}
			for _, wk := range tt.wantKeys {
				if !gotKeys[wk] {
					t.Errorf("missing expected key %q in results", wk)
				}
			}
		})
	}
}

func TestSetWithSubjects_RoundTrip(t *testing.T) {
	store := newTestStore(t)

	subjects := []string{"entity:light.office", "zone:office"}
	fact, err := store.Set(CategoryDevice, "office_light", "Hue desk lamp", "test", 1.0, subjects)
	if err != nil {
		t.Fatal(err)
	}

	if len(fact.Subjects) != 2 {
		t.Fatalf("Set returned fact with %d subjects, want 2", len(fact.Subjects))
	}

	// Retrieve and verify subjects are persisted.
	got, err := store.Get(CategoryDevice, "office_light")
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Subjects) != 2 {
		t.Fatalf("Get returned fact with %d subjects, want 2", len(got.Subjects))
	}
	if got.Subjects[0] != "entity:light.office" || got.Subjects[1] != "zone:office" {
		t.Errorf("subjects = %v, want %v", got.Subjects, subjects)
	}
}

func TestSetWithoutSubjects_NilPreserved(t *testing.T) {
	store := newTestStore(t)

	_, err := store.Set(CategoryPreference, "timezone", "America/Chicago", "test", 1.0, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(CategoryPreference, "timezone")
	if err != nil {
		t.Fatal(err)
	}

	if got.Subjects != nil {
		t.Errorf("expected nil subjects, got %v", got.Subjects)
	}
}
