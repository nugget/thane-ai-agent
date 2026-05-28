package main

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestBuildSubjects_FromBinding(t *testing.T) {
	if got := buildSubjects(wake{}); len(got) != 0 {
		t.Errorf("empty wake should yield no subjects, got %v", got)
	}

	w := wake{Binding: &memory.ChannelBinding{ContactID: "c-abc", Address: "+15551234567"}}
	got := buildSubjects(w)
	want := []string{"contact:c-abc", "contact:+15551234567"}
	mustEqual(t, got, want)
}

func TestBuildSubjects_ExtraThenBinding(t *testing.T) {
	w := wake{
		Binding: &memory.ChannelBinding{ContactID: "c-abc", Address: "+15551234567"},
		ExtraSubjects: []string{
			"entity:binary_sensor.driveway",
			"contact:c-abc", // duplicates the binding's ContactID
		},
	}
	got := buildSubjects(w)
	want := []string{
		"entity:binary_sensor.driveway",
		"contact:c-abc",
		"contact:+15551234567",
	}
	mustEqual(t, got, want)
}

func TestReconstructArchiveQuery(t *testing.T) {
	tests := []struct {
		name       string
		subjects   []string
		message    string
		wantQuery  string
		wantSource string
	}{
		{"subjects strip prefix", []string{"entity:light.office", "zone:kitchen"}, "ignored", "light.office kitchen", "subjects"},
		{"empty subjects, short message", nil, "check the heater", "check the heater", "message_fallback"},
		{"empty subjects, long message dropped", nil, longMsg(120), "", ""},
		{"empty subjects, multiline dropped", nil, "line one\nline two", "", ""},
		{"empty subjects, empty message", nil, "", "", ""},
		{"both — subjects win", []string{"entity:foo"}, "long message content", "foo", "subjects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, src := reconstructArchiveQuery(tt.subjects, tt.message)
			if q != tt.wantQuery || src != tt.wantSource {
				t.Errorf("got (%q, %q); want (%q, %q)", q, src, tt.wantQuery, tt.wantSource)
			}
		})
	}
}

func TestSummarizeArchive_ParsesHits(t *testing.T) {
	body := "### Past Experience\n\n{\"results\":[{\"match\":{}},{\"match\":{}}],\"truncated\":false}"
	pr := summarizeArchive([]string{"entity:x"}, "hello", body)
	if pr.Name != "ArchiveContextProvider" {
		t.Errorf("name = %q", pr.Name)
	}
	if pr.HitCount != 2 {
		t.Errorf("hits = %d, want 2", pr.HitCount)
	}
	if pr.QuerySource != "subjects" {
		t.Errorf("query source = %q, want subjects", pr.QuerySource)
	}
}

func TestSummarizeArchive_ParsesMultiSurfaceHits(t *testing.T) {
	// Post-#983 ArchiveContextProvider shape: distilled hits across
	// messages[]/sessions[]/working_memory[] instead of results[].
	body := "### Past Experience\n\n" +
		`{"messages":[{"m":{}},{"m":{}}],"sessions":[{"s":{}}],"working_memory":[{"w":{}}]}`
	pr := summarizeArchive([]string{"entity:x"}, "hello", body)
	if pr.HitCount != 4 {
		t.Errorf("hits = %d, want 4 (2 messages + 1 session + 1 working_memory)", pr.HitCount)
	}
}

func TestSummarizeArchive_EmptyOutputIsZeroHits(t *testing.T) {
	pr := summarizeArchive(nil, "", "")
	if pr.HitCount != 0 || pr.OutputBytes != 0 {
		t.Errorf("expected zeroed result, got %+v", pr)
	}
}

func mustEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func longMsg(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
