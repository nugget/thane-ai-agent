package email

import "testing"

func TestHTMLToText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "paragraphs and headings",
			in:   "<h1>Title</h1><p>First para.</p><p>Second   para<br>with break.</p>",
			want: "Title\n\nFirst para.\n\nSecond para\nwith break.",
		},
		{
			name: "script and style dropped",
			in:   "<html><head><style>p{color:red}</style><script>alert(1)</script></head><body><p>Visible</p></body></html>",
			want: "Visible",
		},
		{
			name: "links keep target when it differs",
			in:   `<p>Track it <a href="https://example.com/t/1">here</a> or visit <a href="https://example.com">https://example.com</a>.</p>`,
			want: "Track it here (https://example.com/t/1) or visit https://example.com.",
		},
		{
			name: "mailto and anchors omitted",
			in:   `<p>Mail <a href="mailto:alice@example.com">Alice</a> or <a href="#top">jump</a>.</p>`,
			want: "Mail Alice or jump.",
		},
		{
			name: "lists",
			in:   "<ul><li>one</li><li>two</li></ul><p>after</p>",
			want: "- one\n- two\n\nafter",
		},
		{
			name: "entities and inline tags",
			in:   "<p>Tom &amp; Jerry &mdash; <b>bold</b> and <i>italic</i>&nbsp;text</p>",
			want: "Tom & Jerry — bold and italic text",
		},
		{
			name: "entities decode exactly once",
			in:   "<p>literal &amp;amp; stays and &amp;lt;b&amp;gt; is not a tag</p>",
			want: "literal &amp; stays and &lt;b&gt; is not a tag",
		},
		{
			name: "unclosed head does not swallow the body",
			in:   "<html><head><title>Subject</title><style>p{}</style><body><p>Visible body</p></body></html>",
			want: "Visible body",
		},
		{
			name: "table cells separated",
			in:   "<table><tr><td>Item</td><td>Qty</td></tr><tr><td>Apples</td><td>3</td></tr></table>",
			want: "Item\tQty\nApples\t3",
		},
		{
			name: "images use alt text",
			in:   `<p>Logo: <img src="x.png" alt="ACME"> done</p>`,
			want: "Logo: [ACME] done",
		},
		{
			name: "preformatted keeps whitespace",
			in:   "<pre>a  b\n  c</pre>",
			want: "a  b\n  c",
		},
		{
			name: "whitespace collapsed",
			in:   "<div>\n   lots   of\n\n space   </div>",
			want: "lots of space",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := htmlToText(tc.in)
			if got != tc.want {
				t.Errorf("htmlToText(%q) =\n%q\nwant\n%q", tc.in, got, tc.want)
			}
		})
	}
}
