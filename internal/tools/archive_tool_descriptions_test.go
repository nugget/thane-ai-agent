package tools

import (
	"strings"
	"testing"
)

// TestArchiveToolDescriptionsTeachFullSessionIDs pins what each archive
// tool says about session ids. Every result names a session by its full
// id, so each description points at that field; archive_session_transcript
// presents a leading part as a lookup, never as a name; and archive_search
// is named as the way back from a prefix, because sessions imported
// together share leading digits and only content can tell them apart.
func TestArchiveToolDescriptionsTeachFullSessionIDs(t *testing.T) {
	r, _, _ := newArchiveTestRegistry(t)

	for _, tt := range []struct {
		tool string
		want []string
		gone []string
	}{
		{
			tool: "archive_session_transcript",
			want: []string{
				"any leading part of it",
				"returns those candidates with how long ago each started and its title, instead of a transcript",
				"A leading part is only a lookup convenience",
				"anything durable that names a session (a citation, a note) uses the full id",
			},
			gone: []string{"first 8 characters", "longer prefixes are also fine"},
		},
		{
			tool: "archive_search",
			want: []string{
				"Every messages[] and sessions[] hit carries its session's full session_id",
				"use it whole wherever you cite the session",
				"recover the full id behind an id prefix",
			},
		},
		{
			tool: "archive_sessions",
			want: []string{"each entry's id is the full session id", "pass that id to archive_session_transcript"},
		},
		{
			tool: "archive_range",
			want: []string{"every message carries the full session_id"},
		},
		{
			tool: "search",
			want: []string{"archive:session:<full-session-uuid> already names its session in the form a durable citation uses"},
		},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			tool := r.Get(tt.tool)
			if tool == nil {
				t.Fatalf("%s not registered with an archive store", tt.tool)
			}
			for _, want := range tt.want {
				if !strings.Contains(tool.Description, want) {
					t.Errorf("%s description lacks %q: %s", tt.tool, want, tool.Description)
				}
			}
			for _, gone := range tt.gone {
				if strings.Contains(tool.Description, gone) {
					t.Errorf("%s description still says %q: %s", tt.tool, gone, tool.Description)
				}
			}
		})
	}
}
