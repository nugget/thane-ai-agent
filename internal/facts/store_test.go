package facts

import (
	"log/slog"
	"math"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestEmbeddingEncodeDecode(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}

	encoded := encodeEmbedding(original)
	decoded := decodeEmbedding(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}

	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("value %d: got %f, want %f", i, decoded[i], original[i])
		}
	}
}

func TestEmbeddingEncodeEmpty(t *testing.T) {
	if encoded := encodeEmbedding(nil); encoded != nil {
		t.Errorf("expected nil for nil input, got %v", encoded)
	}
	if encoded := encodeEmbedding([]float32{}); encoded != nil {
		t.Errorf("expected nil for empty input, got %v", encoded)
	}
}

func TestEmbeddingDecodeEmpty(t *testing.T) {
	if decoded := decodeEmbedding(nil); decoded != nil {
		t.Errorf("expected nil for nil input, got %v", decoded)
	}
	if decoded := decodeEmbedding([]byte{}); decoded != nil {
		t.Errorf("expected nil for empty input, got %v", decoded)
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{1, 0, 0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 0.0,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{-1, 0, 0},
			expected: -1.0,
		},
		{
			name:     "different lengths",
			a:        []float32{1, 2},
			b:        []float32{1, 2, 3},
			expected: 0.0,
		},
		{
			name:     "zero vector",
			a:        []float32{0, 0, 0},
			b:        []float32{1, 2, 3},
			expected: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(float64(got-tc.expected)) > 0.0001 {
				t.Errorf("got %f, want %f", got, tc.expected)
			}
		})
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "thane-facts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	store, err := NewStore(tmpFile.Name(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestFTS5_Enabled(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available in test environment")
	}
}

func TestFTS5_SearchByKeyword(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	// Insert test facts.
	_, err := store.Set(CategoryHome, "living_room_layout", "Large room with sectional sofa facing the TV", "user", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Set(CategoryDevice, "office_light", "Hue Go lamp on the desk", "observation", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Set(CategoryPreference, "music_volume", "Prefers volume at 40% during work hours", "user", 1.0)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		query   string
		wantMin int // minimum expected results
		wantKey string
	}{
		{
			name:    "search by value word",
			query:   "sofa",
			wantMin: 1,
			wantKey: "living_room_layout",
		},
		{
			name:    "search by key word",
			query:   "office",
			wantMin: 1,
			wantKey: "office_light",
		},
		{
			name:    "search by source",
			query:   "observation",
			wantMin: 1,
			wantKey: "office_light",
		},
		{
			name:    "no results",
			query:   "xyznonexistent",
			wantMin: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.Search(tt.query)
			if err != nil {
				t.Fatalf("Search(%q) error = %v", tt.query, err)
			}
			if len(results) < tt.wantMin {
				t.Errorf("Search(%q) returned %d results, want >= %d", tt.query, len(results), tt.wantMin)
			}
			if tt.wantKey != "" {
				found := false
				for _, f := range results {
					if f.Key == tt.wantKey {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Search(%q) did not return fact with key %q", tt.query, tt.wantKey)
				}
			}
		})
	}
}

func TestFTS5_SearchExcludesDeleted(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	_, err := store.Set(CategoryDevice, "garage_sensor", "Temperature sensor in the garage", "observation", 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's searchable.
	results, err := store.Search("garage")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result before delete, got %d", len(results))
	}

	// Soft-delete the fact.
	if err := store.Delete(CategoryDevice, "garage_sensor"); err != nil {
		t.Fatal(err)
	}

	// Verify it's no longer searchable.
	results, err = store.Search("garage")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

func TestFTS5_SearchAfterUpdate(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	_, err := store.Set(CategoryPreference, "color_temp", "Prefers warm white lights", "user", 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Update the value.
	_, err = store.Set(CategoryPreference, "color_temp", "Prefers cool daylight during work hours", "user", 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Search for the new value.
	results, err := store.Search("daylight")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'daylight', got %d", len(results))
	}
	if results[0].Value != "Prefers cool daylight during work hours" {
		t.Errorf("unexpected value: %s", results[0].Value)
	}

	// Old value should not be found.
	results, err = store.Search("warm")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for old value 'warm', got %d", len(results))
	}
}

func TestSanitizeFTS5Query(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple word", "hello", `"hello"`},
		{"two words", "pool heater", `"pool" "heater"`},
		{"special chars", "models.yaml config", `"models.yaml" "config"`},
		{"empty", "", ""},
		{"with quotes", `say "hello"`, `"say" """hello"""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFTS5Query(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFTS5_LIKEFallbackPath(t *testing.T) {
	// Test the LIKE search path directly (independent of FTS5 availability).
	store := newTestStore(t)

	_, err := store.Set(CategoryHome, "bedroom_size", "Master bedroom is 14x16 feet", "user", 1.0)
	if err != nil {
		t.Fatal(err)
	}

	results, err := store.searchLIKE("bedroom")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("searchLIKE('bedroom') returned %d results, want 1", len(results))
	}
	if results[0].Key != "bedroom_size" {
		t.Errorf("unexpected key: %s", results[0].Key)
	}
}
