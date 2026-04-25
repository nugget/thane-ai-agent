package buildinfo

import "testing"

func TestUserAgent(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3"
	t.Cleanup(func() {
		Version = oldVersion
	})

	got := UserAgent()
	want := "Thane/v1.2.3 (+https://github.com/nugget/thane-ai-agent; automated software agent)"
	if got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

func TestUserAgentForSurface(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3"
	t.Cleanup(func() {
		Version = oldVersion
	})

	got := UserAgentFor(AgentSurfaceForge)
	want := "Thane/v1.2.3 (+https://github.com/nugget/thane-ai-agent; automated software agent; surface=forge)"
	if got != want {
		t.Fatalf("UserAgentFor(AgentSurfaceForge) = %q, want %q", got, want)
	}
}
