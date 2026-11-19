package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
)

func TestProtocol_EraString(t *testing.T) {
	tests := map[era]string{eraUnknown: "unknown", eraLegacy: "legacy", eraModern: "modern", era(42): "unknown"}
	for e, want := range tests {
		if got := e.String(); got != want {
			t.Errorf("era(%d).String() = %q, want %q", int(e), got, want)
		}
	}
}

func TestProtocol_VersionSets(t *testing.T) {
	tests := []struct {
		v         string
		modern    bool
		handshake bool
	}{
		{"2026-07-28", true, false},
		{"2025-11-25", false, true},
		{"2025-06-18", false, true},
		{"2025-03-26", false, true},
		{"2024-11-05", false, true},
		{"2024-10-07", false, false},
		{"2027-01-01", false, false},
		{"draft", false, false},
		{"", false, false},
		{"2025-11-25 ", false, false},
	}
	for _, tt := range tests {
		if got := isModernVersion(tt.v); got != tt.modern {
			t.Errorf("isModernVersion(%q) = %v, want %v", tt.v, got, tt.modern)
		}
		if got := isHandshakeVersion(tt.v); got != tt.handshake {
			t.Errorf("isHandshakeVersion(%q) = %v, want %v", tt.v, got, tt.handshake)
		}
	}
	if !isHandshakeVersion(handshakeOfferVersion) {
		t.Errorf("handshakeOfferVersion %q is not a handshake version", handshakeOfferVersion)
	}
	if handshakeVersions[0] != handshakeOfferVersion {
		t.Errorf("handshakeOfferVersion %q is not the newest handshake version %q", handshakeOfferVersion, handshakeVersions[0])
	}
	if !isModernVersion(modernProtocolVersion) {
		t.Errorf("modernProtocolVersion %q is not modern", modernProtocolVersion)
	}
}

func TestProtocol_VersionAtLeast(t *testing.T) {
	tests := []struct {
		v, floor string
		want     bool
	}{
		{"2025-06-18", headerVersionFloor, true},
		{"2025-11-25", headerVersionFloor, true},
		{"2026-07-28", headerVersionFloor, true},
		{"2025-03-26", headerVersionFloor, false},
		{"2024-11-05", headerVersionFloor, false},
		{"", headerVersionFloor, false},
	}
	for _, tt := range tests {
		if got := versionAtLeast(tt.v, tt.floor); got != tt.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", tt.v, tt.floor, got, tt.want)
		}
	}
}

func TestProtocol_HighestMutualVersion(t *testing.T) {
	tests := []struct {
		name    string
		offered []string
		ours    []string
		want    string
		ok      bool
	}{
		{name: "single modern", offered: []string{"2026-07-28"}, ours: modernVersions, want: "2026-07-28", ok: true},
		{name: "modern among handshake", offered: []string{"2025-11-25", "2026-07-28"}, ours: modernVersions, want: "2026-07-28", ok: true},
		{name: "no modern", offered: []string{"2025-11-25"}, ours: modernVersions, ok: false},
		{name: "highest handshake regardless of order", offered: []string{"2024-11-05", "2025-06-18", "2025-03-26"}, ours: handshakeVersions, want: "2025-06-18", ok: true},
		{name: "unknown versions ignored", offered: []string{"2099-01-01", "2024-11-05"}, ours: handshakeVersions, want: "2024-11-05", ok: true},
		{name: "empty offered", offered: nil, ours: handshakeVersions, ok: false},
		{name: "empty string never matches", offered: []string{""}, ours: []string{""}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := highestMutualVersion(tt.offered, tt.ours)
			if got != tt.want || ok != tt.ok {
				t.Errorf("highestMutualVersion = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestProtocol_ClientInfo(t *testing.T) {
	info := thaneClientInfo()
	if info.Name != clientInfoName || info.Version != buildinfo.Version {
		t.Errorf("thaneClientInfo() = %+v", info)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"name":"thane","version":"` + buildinfo.Version + `"}`; string(b) != want {
		t.Errorf("clientInfo JSON = %s, want %s", b, want)
	}
}

func TestProtocol_ModernMeta(t *testing.T) {
	a := modernMeta()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"io.modelcontextprotocol/clientCapabilities":{},` +
		`"io.modelcontextprotocol/clientInfo":{"name":"thane","version":"` + buildinfo.Version + `"},` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	if string(b) != want {
		t.Errorf("modernMeta JSON = %s, want %s", b, want)
	}

	// Every call returns a fresh map: a caller adding keys (progressToken)
	// must not leak into the next request.
	a["progressToken"] = 1
	if caps, ok := a[metaClientCapabilities].(map[string]any); ok {
		caps["sampling"] = map[string]any{}
	}
	fresh := modernMeta()
	if _, ok := fresh["progressToken"]; ok || len(fresh) != 3 {
		t.Errorf("modernMeta() shares state: %v", fresh)
	}
	if caps, ok := fresh[metaClientCapabilities].(map[string]any); !ok || len(caps) != 0 {
		t.Errorf("clientCapabilities shares state: %v", fresh[metaClientCapabilities])
	}
	for key := range fresh {
		if !strings.HasPrefix(key, "io.modelcontextprotocol/") {
			t.Errorf("unexpected modern _meta key %q", key)
		}
	}
}

func TestProtocol_EncodeHeaderValue(t *testing.T) {
	b64 := func(s string) string { return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?=" }
	tests := []struct {
		in, want string
	}{
		// The spec's own examples.
		{"us-west1", "us-west1"},
		{"Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{" padded ", "=?base64?IHBhZGRlZCA=?="},
		{"line1\nline2", "=?base64?bGluZTEKbGluZTI=?="},
		{"=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
		// Edges.
		{"", ""},
		{"get_weather", "get_weather"},
		{"file:///projects/myapp/config.json", "file:///projects/myapp/config.json"},
		{"inner space", "inner space"},
		{"inner\ttab", "inner\ttab"},
		{"\tlead", b64("\tlead")},
		{"trail\t", b64("trail\t")},
		{" ", b64(" ")},
		{"cr\rlf", b64("cr\rlf")},
		{"nul\x00", b64("nul\x00")},
		{"del\x7f", b64("del\x7f")},
		{"café", b64("café")},
		{"=?base64?", "=?base64?"},
		{"abc?=", "abc?="},
		{"=?BASE64?x?=", "=?BASE64?x?="},
		{"=?base64??=", b64("=?base64??=")},
		{"42", "42"},
		{"true", "true"},
	}
	for _, tt := range tests {
		if got := encodeHeaderValue(tt.in); got != tt.want {
			t.Errorf("encodeHeaderValue(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProtocol_IsHeaderToken(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"Region", true},
		{"X-Tenant_ID", true},
		{"a1", true},
		{"!#$%&'*+-.^_`|~", true},
		{"", false},
		{"has space", false},
		{"colon:", false},
		{"semi;", false},
		{"slash/", false},
		{"quote\"", false},
		{"paren(", false},
		{"brace{", false},
		{"at@", false},
		{"eq=", false},
		{"comma,", false},
		{"q?", false},
		{"bracket[", false},
		{"back\\", false},
		{"tab\t", false},
		{"crlf\r\n", false},
		{"ümlaut", false},
		{"del\x7f", false},
	}
	for _, tt := range tests {
		if got := isHeaderToken(tt.in); got != tt.want {
			t.Errorf("isHeaderToken(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestProtocol_DecodeDiscoverResult(t *testing.T) {
	raw := `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}},` +
		`"instructions":"Use search first.","ttlMs":60000,"cacheScope":"public",` +
		`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"weather","version":"1.2.0","title":"Weather"}}}`
	var r discoverResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.Capabilities == nil || r.Capabilities.Tools == nil || !r.Capabilities.Tools.ListChanged {
		t.Errorf("capabilities = %+v", r.Capabilities)
	}
	if r.TTLMs == nil || *r.TTLMs != 60000 || r.CacheScope != "public" || r.Instructions != "Use search first." {
		t.Errorf("discoverResult = %+v", r)
	}
	info, ok := r.serverInfo()
	if !ok || info != (implementation{Name: "weather", Version: "1.2.0", Title: "Weather"}) {
		t.Errorf("serverInfo() = %+v, %v", info, ok)
	}

	for _, meta := range []string{`{}`, `{"io.modelcontextprotocol/serverInfo":"nope"}`} {
		var r2 discoverResult
		if err := json.Unmarshal([]byte(`{"_meta":`+meta+`}`), &r2); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if _, ok := r2.serverInfo(); ok {
			t.Errorf("serverInfo() ok for _meta %s", meta)
		}
	}
	var noMeta discoverResult
	if _, ok := noMeta.serverInfo(); ok {
		t.Error("serverInfo() ok with no _meta")
	}
}

func TestProtocol_DecodeToolsListCursor(t *testing.T) {
	tests := []struct {
		raw     string
		present bool
		cursor  string
	}{
		{`{"tools":[]}`, false, ""},
		{`{"tools":[],"nextCursor":null}`, false, ""},
		{`{"tools":[],"nextCursor":""}`, true, ""},
		{`{"tools":[],"nextCursor":"page-2"}`, true, "page-2"},
	}
	for _, tt := range tests {
		var r toolsListResult
		if err := json.Unmarshal([]byte(tt.raw), &r); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.raw, err)
		}
		if (r.NextCursor != nil) != tt.present || (r.NextCursor != nil && *r.NextCursor != tt.cursor) {
			t.Errorf("%s: NextCursor = %v, want present=%v %q", tt.raw, r.NextCursor, tt.present, tt.cursor)
		}
	}
}

func TestProtocol_DecodeToolDefinition(t *testing.T) {
	raw := `{"name":"get_weather","title":"Get weather","description":"Current weather.",` +
		`"inputSchema":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}},` +
		`"outputSchema":{"type":"array"},` +
		`"annotations":{"title":"Weather","readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false},` +
		`"_meta":{"vendor/x":1},"icons":[{"src":"x"}],"execution":{"taskSupport":"optional"}}`
	var d toolDefinition
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Name != "get_weather" || d.Title != "Get weather" || d.Description != "Current weather." {
		t.Errorf("toolDefinition = %+v", d)
	}
	if string(d.OutputSchema) != `{"type":"array"}` || string(d.Meta["vendor/x"]) != "1" {
		t.Errorf("outputSchema/_meta = %s / %v", d.OutputSchema, d.Meta)
	}
	a := d.Annotations
	if a == nil || a.Title != "Weather" || a.ReadOnlyHint == nil || !*a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	props, ok := d.InputSchema["properties"].(map[string]any)
	if !ok || props["region"] == nil {
		t.Errorf("inputSchema = %v", d.InputSchema)
	}
}

func TestProtocol_DecodeCallToolResult(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		resultType     string
		structuredNull bool
		structured     bool
		requestState   bool
		inputRequests  int
	}{
		{name: "legacy complete", raw: `{"content":[{"type":"text","text":"hi"}]}`},
		{name: "structured null is present-but-null", raw: `{"content":[],"structuredContent":null}`, structuredNull: true},
		{name: "structured value", raw: `{"content":[],"structuredContent":[1,2]}`, structured: true},
		{name: "input required with state", raw: `{"resultType":"input_required","requestState":"opaque"}`, resultType: resultTypeInputRequired, requestState: true},
		{name: "input required with requests", raw: `{"resultType":"input_required","inputRequests":{"a":{"method":"elicitation/create"}}}`, resultType: resultTypeInputRequired, inputRequests: 1},
		{name: "complete", raw: `{"resultType":"complete","content":[]}`, resultType: resultTypeComplete},
		{name: "task", raw: `{"resultType":"task","task":{"id":"t"}}`, resultType: "task"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r callToolResult
			if err := json.Unmarshal([]byte(tt.raw), &r); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if r.ResultType != tt.resultType {
				t.Errorf("ResultType = %q, want %q", r.ResultType, tt.resultType)
			}
			if got := string(r.StructuredContent) == "null"; got != tt.structuredNull {
				t.Errorf("StructuredContent = %q", r.StructuredContent)
			}
			if tt.structured && string(r.StructuredContent) != "[1,2]" {
				t.Errorf("StructuredContent = %q", r.StructuredContent)
			}
			if (r.RequestState != nil) != tt.requestState {
				t.Errorf("RequestState = %v", r.RequestState)
			}
			if len(r.InputRequests) != tt.inputRequests {
				t.Errorf("InputRequests = %v", r.InputRequests)
			}
		})
	}
}

func TestProtocol_InitializeResultDecode(t *testing.T) {
	raw := `{"protocolVersion":"2025-06-18","capabilities":{"tools":{"listChanged":true},"logging":{}},` +
		`"serverInfo":{"name":"github-mcp-server","version":"0.9.0"},"instructions":"Be careful."}`
	var r initializeResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.ProtocolVersion != "2025-06-18" || r.Capabilities.Tools == nil || !r.Capabilities.Tools.ListChanged ||
		r.ServerInfo.Name != "github-mcp-server" || r.Instructions != "Be careful." {
		t.Errorf("initializeResult = %+v", r)
	}
}
