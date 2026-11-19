package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestJSONRPC_DecodeMessages(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		kinds   []messageKind
		ids     []string // raw id per message ("" = absent)
	}{
		{name: "request int id", in: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, kinds: []messageKind{kindRequest}, ids: []string{"1"}},
		{name: "request string id", in: `{"jsonrpc":"2.0","id":"abc","method":"roots/list"}`, kinds: []messageKind{kindRequest}, ids: []string{`"abc"`}},
		{name: "request id zero", in: `{"jsonrpc":"2.0","id":0,"method":"ping"}`, kinds: []messageKind{kindRequest}, ids: []string{"0"}},
		{name: "notification no id", in: `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`, kinds: []messageKind{kindNotification}, ids: []string{""}},
		{name: "notification null id", in: `{"jsonrpc":"2.0","id":null,"method":"notifications/message"}`, kinds: []messageKind{kindNotification}, ids: []string{"null"}},
		{name: "response result", in: `{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}`, kinds: []messageKind{kindResponse}, ids: []string{"7"}},
		{name: "response null result", in: `{"jsonrpc":"2.0","id":7,"result":null}`, kinds: []messageKind{kindResponse}, ids: []string{"7"}},
		{name: "error response null id", in: `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"bad"}}`, kinds: []messageKind{kindResponse}, ids: []string{"null"}},
		{name: "error response absent id", in: `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Bad Request: Mcp-Session-Id header is required"}}`, kinds: []messageKind{kindResponse}, ids: []string{""}},
		{name: "surrounding whitespace", in: " \r\n\t{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n ", kinds: []messageKind{kindResponse}, ids: []string{"2"}},
		{name: "no method no result", in: `{"jsonrpc":"2.0","id":3}`, kinds: []messageKind{kindInvalid}, ids: []string{"3"}},
		{name: "empty object", in: `{}`, kinds: []messageKind{kindInvalid}, ids: []string{""}},
		{
			name:  "batch of mixed kinds",
			in:    `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","method":"notifications/tools/list_changed"},{"jsonrpc":"2.0","id":"s-1","method":"ping"}]`,
			kinds: []messageKind{kindResponse, kindNotification, kindRequest},
			ids:   []string{"1", "", `"s-1"`},
		},
		{
			name:  "batch keeps valid neighbours of bad elements",
			in:    `[5,"x",null,{"jsonrpc":"2.0","method":5},{"jsonrpc":"2.0","id":9,"result":{}}]`,
			kinds: []messageKind{kindInvalid, kindInvalid, kindInvalid, kindInvalid, kindResponse},
			ids:   []string{"", "", "", "", "9"},
		},
		{name: "empty batch", in: `[]`, wantErr: true},
		{name: "empty input", in: ``, wantErr: true},
		{name: "whitespace only", in: " \n", wantErr: true},
		{name: "invalid json", in: `{"jsonrpc":`, wantErr: true},
		{name: "trailing garbage", in: `{"jsonrpc":"2.0"} x`, wantErr: true},
		{name: "top-level string", in: `"hello"`, wantErr: true},
		{name: "top-level number", in: `42`, wantErr: true},
		{name: "top-level null", in: `null`, wantErr: true},
		{name: "plain log line", in: `npm WARN deprecated something`, wantErr: true},
		{name: "object with wrong field type", in: `{"jsonrpc":"2.0","method":5}`, wantErr: true},
		{name: "malformed batch", in: `[{"jsonrpc":"2.0"},`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := decodeMessages([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("decodeMessages(%q) = %d messages, want error", tt.in, len(msgs))
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeMessages(%q) error: %v", tt.in, err)
			}
			if len(msgs) != len(tt.kinds) {
				t.Fatalf("got %d messages, want %d", len(msgs), len(tt.kinds))
			}
			for i := range msgs {
				if got := msgs[i].kind(); got != tt.kinds[i] {
					t.Errorf("message %d kind = %v, want %v", i, got, tt.kinds[i])
				}
				if got := string(msgs[i].ID); got != tt.ids[i] {
					t.Errorf("message %d id = %q, want %q", i, got, tt.ids[i])
				}
			}
		})
	}
}

func TestJSONRPC_Kind(t *testing.T) {
	result := json.RawMessage(`{}`)
	rpcErr := &RPCError{Code: -1}
	tests := []struct {
		name string
		m    wireMessage
		want messageKind
	}{
		{name: "method and id", m: wireMessage{Method: "ping", ID: json.RawMessage(`1`)}, want: kindRequest},
		{name: "method and padded id", m: wireMessage{Method: "ping", ID: json.RawMessage(` 1 `)}, want: kindRequest},
		{name: "method and null id", m: wireMessage{Method: "ping", ID: json.RawMessage(`null`)}, want: kindNotification},
		{name: "method only", m: wireMessage{Method: "x"}, want: kindNotification},
		{name: "method wins over result", m: wireMessage{Method: "x", ID: json.RawMessage(`1`), Result: result}, want: kindRequest},
		{name: "result only", m: wireMessage{Result: result}, want: kindResponse},
		{name: "error only", m: wireMessage{Error: rpcErr}, want: kindResponse},
		{name: "result and error", m: wireMessage{ID: json.RawMessage(`1`), Result: result, Error: rpcErr}, want: kindResponse},
		{name: "id only", m: wireMessage{ID: json.RawMessage(`1`)}, want: kindInvalid},
		{name: "zero", m: wireMessage{}, want: kindInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.kind(); got != tt.want {
				t.Errorf("kind() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJSONRPC_KindString(t *testing.T) {
	tests := map[messageKind]string{
		kindInvalid:      "invalid",
		kindRequest:      "request",
		kindNotification: "notification",
		kindResponse:     "response",
		messageKind(99):  "invalid",
	}
	for k, want := range tests {
		if got := k.String(); got != want {
			t.Errorf("messageKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

func TestJSONRPC_Conversions(t *testing.T) {
	msgs, err := decodeMessages([]byte(`[
		{"jsonrpc":"2.0","id":"req-1","method":"sampling/createMessage","params":{"a":1}},
		{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}},
		{"jsonrpc":"2.0","id":4,"result":{"ok":true}},
		{"jsonrpc":"2.0","id":5,"error":{"code":-32602,"message":"bad","data":{"x":[1]}}}
	]`))
	if err != nil {
		t.Fatalf("decodeMessages: %v", err)
	}

	req := msgs[0].request()
	if req.JSONRPC != "2.0" || string(req.ID) != `"req-1"` || req.Method != methodSampling || string(req.Params) != `{"a":1}` {
		t.Errorf("request() = %+v", req)
	}
	n := msgs[1].notification()
	if n.JSONRPC != "2.0" || n.Method != methodLogMessage || string(n.Params) != `{"level":"info"}` {
		t.Errorf("notification() = %+v", n)
	}
	resp := msgs[2].response()
	if string(resp.ID) != "4" || string(resp.Result) != `{"ok":true}` || resp.Error != nil {
		t.Errorf("response() = %+v", resp)
	}
	errResp := msgs[3].response()
	if errResp.Error == nil || errResp.Error.Code != codeInvalidParams || errResp.Error.Message != "bad" || string(errResp.Error.Data) != `{"x":[1]}` {
		t.Errorf("error response() = %+v", errResp)
	}
}

func TestJSONRPC_DecodeCopiesInput(t *testing.T) {
	buf := []byte(`{"jsonrpc":"2.0","id":1,"result":{"v":"before"}}`)
	msgs, err := decodeMessages(buf)
	if err != nil {
		t.Fatalf("decodeMessages: %v", err)
	}
	for i := range buf {
		buf[i] = 'x'
	}
	if got := string(msgs[0].Result); got != `{"v":"before"}` {
		t.Errorf("Result aliases the input buffer: %q", got)
	}
}

func TestJSONRPC_HasID(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"null", false},
		{" null ", false},
		{"0", true},
		{"1", true},
		{`""`, true},
		{`"x"`, true},
		{"-3", true},
	}
	for _, tt := range tests {
		var raw json.RawMessage
		if tt.raw != "" {
			raw = json.RawMessage(tt.raw)
		}
		if got := hasID(raw); got != tt.want {
			t.Errorf("hasID(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestJSONRPC_IntIDRoundTrip(t *testing.T) {
	for _, n := range []int64{0, 1, 42, 1 << 40, 9223372036854775807, -1} {
		raw := intID(n)
		if !json.Valid(raw) {
			t.Fatalf("intID(%d) = %q is not valid JSON", n, raw)
		}
		got, ok := idInt(raw)
		if !ok || got != n {
			t.Errorf("idInt(intID(%d)) = %d, %v", n, got, ok)
		}
	}
	if got := string(intID(17)); got != "17" {
		t.Errorf("intID(17) = %q", got)
	}
}

func TestJSONRPC_IDInt(t *testing.T) {
	tests := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"1", 1, true},
		{" 12 ", 12, true},
		{"-5", -5, true},
		{"9223372036854775807", 9223372036854775807, true},
		{"9223372036854775808", 0, false},
		{"1.0", 0, false},
		{"1e3", 0, false},
		{`"1"`, 0, false},
		{`"abc"`, 0, false},
		{"null", 0, false},
		{"", 0, false},
		{"+1", 0, false},
		{"-", 0, false},
		{"true", 0, false},
		{"{}", 0, false},
	}
	for _, tt := range tests {
		got, ok := idInt(json.RawMessage(tt.raw))
		if ok != tt.ok || got != tt.want {
			t.Errorf("idInt(%q) = %d, %v; want %d, %v", tt.raw, got, ok, tt.want, tt.ok)
		}
	}
}

func TestJSONRPC_MarshalParams(t *testing.T) {
	var nilMap map[string]any
	tests := []struct {
		name    string
		in      any
		want    string // "" = nil result
		wantErr bool
	}{
		{name: "nil", in: nil},
		{name: "nil map", in: nilMap},
		{name: "empty map", in: map[string]any{}, want: `{}`},
		{name: "map", in: map[string]any{"cursor": ""}, want: `{"cursor":""}`},
		{name: "raw message", in: json.RawMessage(`{ "a" : 1 }`), want: `{"a":1}`},
		{name: "struct", in: struct {
			Name string `json:"name"`
		}{Name: "echo"}, want: `{"name":"echo"}`},
		{name: "unmarshalable", in: map[string]any{"c": make(chan int)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := marshalParams(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("marshalParams = %q, want error", got)
				}
				// The caller's class layer names mcp: once per chain.
				if strings.Contains(err.Error(), "mcp:") {
					t.Errorf("marshalParams error %q repeats the mcp: prefix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("marshalParams: %v", err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("marshalParams = %q, want nil", got)
				}
				return
			}
			if string(got) != tt.want {
				t.Errorf("marshalParams = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJSONRPC_WireShapes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			name: "request without params omits params",
			v:    &Request{JSONRPC: jsonrpcVersion, ID: intID(3), Method: methodPing},
			want: `{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		},
		{
			name: "request with params",
			v:    &Request{JSONRPC: jsonrpcVersion, ID: intID(4), Method: methodToolsList, Params: json.RawMessage(`{"cursor":""}`)},
			want: `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"cursor":""}}`,
		},
		{
			name: "notification",
			v:    &Notification{JSONRPC: jsonrpcVersion, Method: methodInitialized},
			want: `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		},
		{
			name: "reply with string id",
			v:    &Response{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`"s-9"`), Result: json.RawMessage(`{}`)},
			want: `{"jsonrpc":"2.0","id":"s-9","result":{}}`,
		},
		{
			name: "error reply",
			v:    &Response{JSONRPC: jsonrpcVersion, ID: intID(1), Error: &RPCError{Code: codeMethodNotFound, Message: "Method not found"}},
			want: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("Marshal = %s, want %s", b, tt.want)
			}
		})
	}
}

func TestJSONRPC_RPCErrorString(t *testing.T) {
	short := &RPCError{Code: codeInvalidParams, Message: "Unknown tool: nope"}
	if got, want := short.Error(), "jsonrpc error -32602: Unknown tool: nope"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	long := &RPCError{Code: -32000, Message: strings.Repeat("a", rpcErrorMessageCap-1) + "é" + strings.Repeat("b", 100)}
	got := long.Error()
	prefix := "jsonrpc error -32000: "
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("Error() = %q", got)
	}
	msg := strings.TrimPrefix(got, prefix)
	if len(msg) > rpcErrorMessageCap {
		t.Errorf("message part is %d bytes, want <= %d", len(msg), rpcErrorMessageCap)
	}
	if !utf8.ValidString(msg) {
		t.Errorf("message part is not valid UTF-8: %q", msg[len(msg)-4:])
	}
	if msg != strings.Repeat("a", rpcErrorMessageCap-1) {
		t.Errorf("cut did not step back to the rune start: ends %q", msg[len(msg)-4:])
	}
}
