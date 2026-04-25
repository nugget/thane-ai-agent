package media

import (
	"testing"
)

func TestCleanVTT_Empty(t *testing.T) {
	if got := CleanVTT(""); got != "" {
		t.Errorf("CleanVTT(\"\") = %q, want empty", got)
	}
}

func TestCleanVTT_StripHeaders(t *testing.T) {
	raw := "WEBVTT\nKind: captions\nLanguage: en\n\n00:00:01.000 --> 00:00:03.000\nHello world"
	got := CleanVTT(raw)
	if got != "Hello world" {
		t.Errorf("CleanVTT = %q, want %q", got, "Hello world")
	}
}

func TestCleanVTT_StripHTMLTags(t *testing.T) {
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n<font color=\"#ffffff\">Hello</font> <c>world</c>"
	got := CleanVTT(raw)
	if got != "Hello world" {
		t.Errorf("CleanVTT = %q, want %q", got, "Hello world")
	}
}

func TestCleanVTT_StripCueIDs(t *testing.T) {
	raw := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.000\nFirst line\n\n2\n00:00:03.000 --> 00:00:05.000\nSecond line"
	got := CleanVTT(raw)
	if got != "First line Second line" {
		t.Errorf("CleanVTT = %q, want %q", got, "First line Second line")
	}
}

func TestCleanVTT_DeduplicateRollingCaptions(t *testing.T) {
	// Auto-generated subs repeat text across overlapping cues.
	raw := `WEBVTT

00:00:01.000 --> 00:00:03.000
Hello everyone welcome to the show

00:00:02.000 --> 00:00:05.000
Hello everyone welcome to the show

00:00:04.000 --> 00:00:07.000
today we are going to talk about AI

00:00:06.000 --> 00:00:09.000
today we are going to talk about AI

00:00:08.000 --> 00:00:11.000
and how it changes everything`

	got := CleanVTT(raw)
	want := "Hello everyone welcome to the show today we are going to talk about AI and how it changes everything"
	if got != want {
		t.Errorf("CleanVTT dedup:\n got: %q\nwant: %q", got, want)
	}
}

func TestCleanVTT_AlreadyCleanText(t *testing.T) {
	// Plain text without VTT formatting passes through.
	raw := "Just some plain text"
	got := CleanVTT(raw)
	if got != "Just some plain text" {
		t.Errorf("CleanVTT = %q, want %q", got, "Just some plain text")
	}
}

func TestCleanVTT_PositionMetadata(t *testing.T) {
	// Timing lines can have position/alignment metadata after timestamps.
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000 position:10% align:start\nHello world"
	got := CleanVTT(raw)
	if got != "Hello world" {
		t.Errorf("CleanVTT = %q, want %q", got, "Hello world")
	}
}

func TestCleanVTTWithParagraphs_Empty(t *testing.T) {
	if got := CleanVTTWithParagraphs(""); got != "" {
		t.Errorf("CleanVTTWithParagraphs(\"\") = %q, want empty", got)
	}
}

func TestCleanVTTWithParagraphs_GapInsertsParagraph(t *testing.T) {
	// A 3-second gap between cues should produce a paragraph break.
	raw := `WEBVTT

00:00:01.000 --> 00:00:03.000
First topic sentence.

00:00:03.000 --> 00:00:05.000
Still first topic.

00:00:10.000 --> 00:00:12.000
New topic after a gap.

00:00:12.000 --> 00:00:14.000
More about new topic.`

	got := CleanVTTWithParagraphs(raw)
	want := "First topic sentence. Still first topic.\n\nNew topic after a gap. More about new topic."
	if got != want {
		t.Errorf("CleanVTTWithParagraphs:\n got: %q\nwant: %q", got, want)
	}
}

func TestCleanVTTWithParagraphs_NoParagraphForSmallGap(t *testing.T) {
	// A 1-second gap should NOT produce a paragraph break.
	raw := `WEBVTT

00:00:01.000 --> 00:00:03.000
Line one.

00:00:04.000 --> 00:00:06.000
Line two.`

	got := CleanVTTWithParagraphs(raw)
	want := "Line one. Line two."
	if got != want {
		t.Errorf("CleanVTTWithParagraphs:\n got: %q\nwant: %q", got, want)
	}
}

func TestCleanVTTWithParagraphs_Deduplicates(t *testing.T) {
	raw := `WEBVTT

00:00:01.000 --> 00:00:03.000
Hello world

00:00:02.000 --> 00:00:04.000
Hello world

00:00:03.000 --> 00:00:05.000
Goodbye world`

	got := CleanVTTWithParagraphs(raw)
	want := "Hello world Goodbye world"
	if got != want {
		t.Errorf("CleanVTTWithParagraphs dedup:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseTimestampMs(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"00:00:00.000", 0},
		{"00:00:01.000", 1000},
		{"00:01:00.000", 60000},
		{"01:00:00.000", 3600000},
		{"01:23:45.678", 5025678},
		{"", 0},
		{"short", 0},
	}

	for _, tt := range tests {
		got := parseTimestampMs(tt.input)
		if got != tt.want {
			t.Errorf("parseTimestampMs(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
