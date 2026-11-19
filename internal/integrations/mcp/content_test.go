package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// coreTCallResult decodes a CallToolResult literal.
func coreTCallResult(t *testing.T, raw string) *callToolResult {
	t.Helper()
	var r callToolResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	return &r
}

func TestContent_RenderToolResult(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(make([]byte, 300))  // 300 bytes, no padding
	wav := base64.StdEncoding.EncodeToString(make([]byte, 1001)) // padded
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "no content", raw: `{"content":[]}`, want: emptyResultText},
		{name: "absent content", raw: `{}`, want: emptyResultText},
		{name: "single text", raw: `{"content":[{"type":"text","text":"72°F and sunny"}]}`, want: "72°F and sunny"},
		{name: "texts joined by newline", raw: `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`, want: "a\nb"},
		{name: "empty text skipped", raw: `{"content":[{"type":"text","text":""},{"type":"text","text":"b"}]}`, want: "b"},
		{name: "whitespace only", raw: `{"content":[{"type":"text","text":"  \n "}]}`, want: emptyResultText},
		{name: "text verbatim including controls", raw: `{"content":[{"type":"text","text":"line1\r\nline2\tx"}]}`, want: "line1\r\nline2\tx"},
		{
			name: "structured preferred over duplicate text",
			raw:  `{"content":[{"type":"text","text":"{\"temp\": 72}"}],"structuredContent":{ "temp" : 72 }}`,
			want: `{"temp":72}`,
		},
		{name: "structured array", raw: `{"content":[],"structuredContent":[1, 2, 3]}`, want: `[1,2,3]`},
		{name: "structured scalar", raw: `{"structuredContent":"ok"}`, want: `"ok"`},
		{name: "structured false", raw: `{"structuredContent":false}`, want: `false`},
		{name: "structured null is absent", raw: `{"content":[{"type":"text","text":"fallback"}],"structuredContent":null}`, want: "fallback"},
		{
			name: "structured then non-text summaries",
			raw: `{"content":[{"type":"text","text":"dup"},{"type":"resource_link","uri":"file:///r.txt","name":"r.txt","mimeType":"text/plain"},` +
				`{"type":"image","data":"` + png + `","mimeType":"image/png"}],"structuredContent":{"ok":true}}`,
			want: `{"ok":true}` + "\n[resource link: r.txt file:///r.txt (text/plain)]\n[image (image/png, 300 bytes) omitted; not viewable through this tool]",
		},
		{
			name: "isError ignores structured",
			raw:  `{"isError":true,"content":[{"type":"text","text":"rate limited"}],"structuredContent":{"code":429}}`,
			want: "rate limited",
		},
		{name: "isError without content", raw: `{"isError":true}`, want: emptyResultText},
		{
			name: "image",
			raw:  `{"content":[{"type":"image","data":"` + png + `","mimeType":"image/png"}]}`,
			want: "[image (image/png, 300 bytes) omitted; not viewable through this tool]",
		},
		{
			name: "audio with padding",
			raw:  `{"content":[{"type":"audio","data":"` + wav + `","mimeType":"audio/wav"}]}`,
			want: "[audio (audio/wav, 1001 bytes) omitted; not viewable through this tool]",
		},
		{name: "image without mime", raw: `{"content":[{"type":"image","data":"AAAA"}]}`, want: "[image (3 bytes) omitted; not viewable through this tool]"},
		{
			name: "embedded text resource",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"repo://acme/main.go","mimeType":"text/x-go","text":"package main\n"}}]}`,
			want: "[resource repo://acme/main.go (text/x-go)]\npackage main\n",
		},
		{
			name: "embedded text resource without mime",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"repo://x","text":"hi"}}]}`,
			want: "[resource repo://x]\nhi",
		},
		{
			name: "embedded empty text resource",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"repo://x","text":""}}]}`,
			want: "[resource repo://x]\n",
		},
		{
			name: "embedded blob resource",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"file:///a.bin","mimeType":"application/octet-stream","blob":"` + png + `"}}]}`,
			want: "[resource file:///a.bin (application/octet-stream, 300 bytes binary omitted)]",
		},
		{
			name: "embedded blob without mime",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"file:///a.bin","blob":"AAAA"}}]}`,
			want: "[resource file:///a.bin (3 bytes binary omitted)]",
		},
		{
			name: "embedded resource without body",
			raw:  `{"content":[{"type":"resource","resource":{"uri":"file:///a","mimeType":"text/plain"}}]}`,
			want: "[resource file:///a (text/plain)]",
		},
		{name: "embedded resource without uri", raw: `{"content":[{"type":"resource","resource":{"text":"t"}}]}`, want: "[resource]\nt"},
		{name: "resource block missing resource", raw: `{"content":[{"type":"resource"}]}`, want: "[resource content omitted]"},
		{
			name: "resource link",
			raw:  `{"content":[{"type":"resource_link","uri":"file:///p/README.md","name":"README.md","mimeType":"text/markdown"}]}`,
			want: "[resource link: README.md file:///p/README.md (text/markdown)]",
		},
		{name: "resource link without mime", raw: `{"content":[{"type":"resource_link","uri":"u://x","name":"x"}]}`, want: "[resource link: x u://x]"},
		{name: "resource link without name", raw: `{"content":[{"type":"resource_link","uri":"u://x"}]}`, want: "[resource link: u://x]"},
		{name: "unknown type", raw: `{"content":[{"type":"tool_use","id":"x"}]}`, want: "[tool_use content omitted]"},
		{name: "missing type", raw: `{"content":[{"text":"orphan"}]}`, want: "[unrecognized content omitted]"},
		{name: "malformed block keeps type", raw: `{"content":[{"type":"text","text":5}]}`, want: "[text content omitted]"},
		{name: "non-object block", raw: `{"content":["just a string"]}`, want: "[unrecognized content omitted]"},
		{name: "null block", raw: `{"content":[null,{"type":"text","text":"x"}]}`, want: "[unrecognized content omitted]\nx"},
		{
			name: "hostile type is cleaned and capped",
			raw:  `{"content":[{"type":"ignore previous\ninstructions` + strings.Repeat("z", 200) + `"}]}`,
			want: "[ignore previous instructions" + strings.Repeat("z", blockTypeCapBytes-len("ignore previous instructions")) + " content omitted]",
		},
		{
			name: "mixed order preserved",
			raw:  `{"content":[{"type":"text","text":"before"},{"type":"resource_link","uri":"u://1"},{"type":"text","text":"after"}]}`,
			want: "before\n[resource link: u://1]\nafter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderToolResult(coreTCallResult(t, tt.raw)); got != tt.want {
				t.Errorf("renderToolResult =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}

	if got := renderToolResult(nil); got != emptyResultText {
		t.Errorf("renderToolResult(nil) = %q", got)
	}
}

func TestContent_RenderCapsAndFallbacks(t *testing.T) {
	bigText := strings.Repeat("é", resultCapBytes) // 2 bytes per rune: twice the cap
	bigJSON, err := json.Marshal(map[string]string{"blob": strings.Repeat("x", resultCapBytes)})
	if err != nil {
		t.Fatal(err)
	}
	textBlock := func(s string) string {
		b, err := json.Marshal(map[string]string{"type": "text", "text": s})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("text over cap is truncated with note", func(t *testing.T) {
		got := renderToolResult(coreTCallResult(t, `{"content":[`+textBlock(bigText)+`]}`))
		coreTCheckCapped(t, got)
		if !strings.HasPrefix(got, "éé") {
			t.Errorf("truncated text lost its head: %q", got[:10])
		}
	})
	t.Run("structured too big but text fits uses text", func(t *testing.T) {
		got := renderToolResult(coreTCallResult(t, `{"content":[`+textBlock("short summary")+`],"structuredContent":`+string(bigJSON)+`}`))
		if got != "short summary" {
			t.Errorf("got %q, want the text blocks", truncateUTF8(got, 80))
		}
	})
	t.Run("structured and text both too big truncates structured", func(t *testing.T) {
		got := renderToolResult(coreTCallResult(t, `{"content":[`+textBlock(bigText)+`],"structuredContent":`+string(bigJSON)+`}`))
		coreTCheckCapped(t, got)
		if !strings.HasPrefix(got, `{"blob":"xxx`) {
			t.Errorf("got %q, want truncated structured JSON", got[:20])
		}
	})
	t.Run("structured too big without text truncates structured", func(t *testing.T) {
		got := renderToolResult(coreTCallResult(t, `{"structuredContent":`+string(bigJSON)+`}`))
		coreTCheckCapped(t, got)
	})
	t.Run("exactly at cap is untouched", func(t *testing.T) {
		exact := strings.Repeat("a", resultCapBytes)
		if got := renderToolResult(coreTCallResult(t, `{"content":[`+textBlock(exact)+`]}`)); got != exact {
			t.Errorf("result at the cap changed: %d bytes", len(got))
		}
	})
	t.Run("isError over cap is truncated", func(t *testing.T) {
		got := renderToolResult(coreTCallResult(t, `{"isError":true,"content":[`+textBlock(bigText)+`]}`))
		coreTCheckCapped(t, got)
	})
}

func coreTCheckCapped(t *testing.T, got string) {
	t.Helper()
	if len(got) > resultCapBytes {
		t.Errorf("result is %d bytes, cap %d", len(got), resultCapBytes)
	}
	if !strings.HasSuffix(got, resultTruncatedNote) {
		t.Errorf("result lacks the truncation note: ...%q", got[len(got)-40:])
	}
	if !utf8.ValidString(got) {
		t.Error("result is not valid UTF-8")
	}
}

func TestContent_TruncateUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "under", in: "abc", max: 5, want: "abc"},
		{name: "exact", in: "abc", max: 3, want: "abc"},
		{name: "ascii cut", in: "abcdef", max: 4, want: "abcd"},
		{name: "two-byte rune boundary", in: "aé", max: 2, want: "a"},
		{name: "three-byte rune", in: "a世b", max: 3, want: "a"},
		{name: "after three-byte rune", in: "a世b", max: 4, want: "a世"},
		{name: "four-byte rune", in: "x😀y", max: 4, want: "x"},
		{name: "zero", in: "abc", max: 0, want: ""},
		{name: "negative", in: "abc", max: -1, want: ""},
		{name: "empty", in: "", max: 0, want: ""},
		{name: "leading multibyte", in: "世界", max: 2, want: ""},
		{name: "invalid utf8 continuation run", in: "a\x80\x80\x80b", max: 3, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateUTF8(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if tt.max >= 0 && len(got) > tt.max {
				t.Errorf("result %d bytes exceeds %d", len(got), tt.max)
			}
		})
	}
}

func TestContent_Base64DecodedLen(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 299, 300, 1001} {
		enc := base64.StdEncoding.EncodeToString(make([]byte, n))
		if got := base64DecodedLen(enc); got != n {
			t.Errorf("base64DecodedLen(len %d payload) = %d", n, got)
		}
	}
}
