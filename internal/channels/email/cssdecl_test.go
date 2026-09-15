package email

import (
	"strings"
	"testing"
)

// TestInlineStyleSplitsLikeABrowser checks that one style attribute is
// split into declarations the way the CSS tokenizer splits it. Under
// declaration precedence a declaration the renderer invents or misreads
// can override a real one, so each style is judged by what a browser
// does to the text of the span it is on: hidden styles hide it, shown
// styles show it.
func TestInlineStyleSplitsLikeABrowser(t *testing.T) {
	cases := []struct {
		name   string
		hidden []string
		shown  []string
	}{
		{
			name: "an escaped bang starts no !important",
			hidden: []string{
				`display:block\!important;display:none`,
				`opacity:1\!important;opacity:0`,
				`visibility:visible\!important;visibility:hidden`,
				`font-size:12px\!important;font-size:0`,
				// An escaped backslash leaves the bang real, and the
				// value it leaves, block\, is no keyword.
				`display:none;display:block\\!important`,
			},
			shown: []string{`display:none\!important;display:block`},
		},
		{
			name: "a genuine !important in any spelling",
			hidden: []string{
				`display:none!\69mportant;display:block`,
				`display:none! important;display:block`,
				`display:none!/**/important;display:block`,
				`display:none !IMPORTANT ;display:block`,
			},
		},
		{
			name: "a semicolon inside a string, block, url or escape ends no declaration",
			hidden: []string{
				`display:none;--x:";display:block;"`,
				`display:none;x:'a;display:block;b'`,
				`display:none;x:"a\";display:block;"`,
				`display:none;x:f(;display:block;)`,
				`display:none;x:f(;display:block)`,
				`display:none;x:(];display:block;)`,
				`display:none;x:[;display:block;]`,
				`display:none;x:{;display:block;}`,
				`display:none;background:url(a;display:block;)`,
				`display:none;background:url(a\);display:block;)`,
				`display:none;x:a\;display:block`,
			},
			shown: []string{
				`display:none;x:'a;b';display:block`,
				`display:none;x:f(a;b);display:block`,
				`display:none;x:{};display:block`,
				`display:none;background:url(a;b);display:block`,
				`display:none;background:url( "a;b" );display:block`,
				`display:none;x:a\\;display:block`,
			},
		},
		{
			name: "an escaped colon or space ends no property name",
			hidden: []string{
				`display:none;display\:block`,
				`display:none;display\20:block`,
			},
			shown: []string{`display:none;displ\61 y:block`},
		},
		{
			name: "a comment separates tokens and is text inside a string",
			hidden: []string{
				`display:none;display:bl/**/ock`,
				`display:none;dis/**/play:block`,
				`display:block;x:"/*";display:none;y:"*/"`,
			},
			shown: []string{
				`display:none;display:/* note */block`,
				`display:none;x:"/*";display:block;y:"*/"`,
			},
		},
		{
			name: "a keyword matches whole and ASCII case-insensitively",
			hidden: []string{
				`display:none;display:bloc\212a`,
				`display:none;display:block\20`,
				`display:none;display:block\a0`,
				`display:none;display:block&nbsp;`,
				`display:none;display:block\`,
			},
			shown: []string{
				`display:none;DISPLAY:BLOCK`,
				`display:none;display:bl\6f ck`,
			},
		},
		{
			name: "a number written with an escape is trusted only to hide",
			hidden: []string{
				`opacity:0;opacity:\31`,
				`font-size:0;font-size:\31 2px`,
				`font-size:0;font-size:12p\x`,
				`opacity:1;opacity:\30`,
			},
		},
		{
			name: "a newline ends a string, and CR LF is one newline",
			hidden: []string{
				"display:block;x:\"a\n;display:none",
				`display:\6e&#13;&#10;one`,
			},
			shown: []string{"display:none;x:\"a\n;display:block"},
		},
		{
			name: "a url token swallows brackets and a function does not",
			hidden: []string{
				`display:none;background:xurl(a[);display:block`,
				`display:none;background:url/**/(a[);display:block`,
				`display:none;background:url('a' [);display:block`,
				`display:block;background:url(a[);display:none`,
			},
			shown: []string{
				`display:none;background:url(a[);display:block`,
				`display:none;background:URL(a[);display:block`,
				`display:none;background:u\72 l(a[);display:block`,
			},
		},
		{
			name:   "a block left open runs to the end",
			hidden: []string{`display:none;x:(];display:block)`},
			shown:  []string{`display:block;x:(;display:none`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, style := range tc.hidden {
				assertSpanShown(t, style, false)
			}
			for _, style := range tc.shown {
				assertSpanShown(t, style, true)
			}
		})
	}
}

// assertSpanShown renders a one-character span with the given style
// between two visible letters and checks whether its text reaches the
// model.
func assertSpanShown(t *testing.T, style string, shown bool) {
	t.Helper()
	in := `<p>A<span style="` + strings.ReplaceAll(style, `"`, "&quot;") + `">x</span>Z</p>`
	want, wantHidden := "AZ", 1
	if shown {
		want, wantHidden = "AxZ", 0
	}
	got, hidden := htmlToText(in)
	if got != want || hidden != wantHidden {
		t.Errorf("style %q: text %q, hidden %d; want %q, hidden %d", style, got, hidden, want, wantHidden)
	}
}
