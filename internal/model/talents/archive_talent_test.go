package talents

import (
	"strings"
	"testing"
)

// TestArchiveTalentsTeachFullSessionIDs pins how the archive talents
// name a session: by the full session_id a result carries, with a
// leading part taught only as a lookup that may answer with several
// sessions, and content search as the way to choose among them. It also
// pins that the lines teaching the 8-character prefix as the way to
// name a session, and the old "longer values are full ids" rule, are
// gone.
func TestArchiveTalentsTeachFullSessionIDs(t *testing.T) {
	want := []string{"archive_text", "archive_session"}
	text := make(map[string]string)
	for _, talent := range loadRepoTalents(t) {
		for _, name := range want {
			if talent.Name == name {
				text[name] = strings.Join(strings.Fields(talent.Content), " ")
			}
		}
	}
	for _, name := range want {
		if _, ok := text[name]; !ok {
			t.Fatalf("talent %q not loaded; the guard would be meaningless", name)
		}
	}

	for _, tt := range []struct {
		name   string
		talent string
		want   string
	}{
		{"transcript takes the full id from the result", "archive_session", "passing the full session id from the result that led you there"},
		{"a leading part is a lookup", "archive_session", "Treat it as a lookup convenience, never as the session's name"},
		{"imports share leading digits", "archive_session", "sessions imported together share their leading digits"},
		{"several matches return candidates", "archive_session", "returns the candidates — full id, how long ago each started"},
		{"candidates carry an age, not a timestamp", "archive_session", "`time_basis` `session_started`"},
		{"durable names carry the full id", "archive_session", "anything durable that names a session — a dossier citation, a note, a handoff — carries the full id"},
		{"search hits carry the full id", "archive_text", "hit names its session by the full `session_id`"},
		{"content search recovers a prefix", "archive_text", "search for the words of the claim itself and keep the hit whose `session_id` begins with that prefix"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	for _, gone := range []string{
		"or an 8-character prefix",
		"won't prefix-match",
		`"session_id": "019e6238"`,
	} {
		for name, body := range text {
			if strings.Contains(body, gone) {
				t.Errorf("talent %s still contains %q", name, gone)
			}
		}
	}
}
