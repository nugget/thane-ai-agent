package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// jsonrpcVersion is the JSON-RPC protocol version used by MCP.
const jsonrpcVersion = "2.0"

// Request is a JSON-RPC 2.0 request.
//
// Outbound (client to server): callers set Method and Params only.
// Transport.Call stamps JSONRPC and a fresh integer ID on its own copy
// and never mutates the caller's value.
//
// Inbound (server to client): ID is kept verbatim (string or number)
// and echoed unchanged in the reply.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification (no ID, never answered).
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response. ID may be absent or null on an
// error response; the HTTP transport correlates by POST, not by ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// rpcErrorMessageCap bounds the server-authored message in
// [RPCError.Error], which ends up in logs.
const rpcErrorMessageCap = 512

// Error implements error. The message is rune-safely truncated to 512
// bytes. It is operator/log text and never reaches the model verbatim.
func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, truncateUTF8(e.Message, rpcErrorMessageCap))
}

// messageKind classifies an inbound wireMessage.
type messageKind int

const (
	kindInvalid messageKind = iota
	kindRequest
	kindNotification
	kindResponse
)

// String returns a short name for logs.
func (k messageKind) String() string {
	switch k {
	case kindRequest:
		return "request"
	case kindNotification:
		return "notification"
	case kindResponse:
		return "response"
	default:
		return "invalid"
	}
}

// wireMessage is the inbound union of request, notification and
// response. An ID of JSON null counts as absent (see hasID).
type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// kind classifies m: a request has a method and a non-null id, a
// notification has a method and no (or a null) id, and a response has
// no method and a result or an error. A "result": null still counts as
// a result, because encoding/json stores it as the bytes "null". Any
// other shape is kindInvalid.
func (m *wireMessage) kind() messageKind {
	switch {
	case m.Method != "" && hasID(m.ID):
		return kindRequest
	case m.Method != "":
		return kindNotification
	case m.Result != nil || m.Error != nil:
		return kindResponse
	default:
		return kindInvalid
	}
}

// request converts m to a Request, keeping the ID verbatim. Call it only
// after kind reports kindRequest.
func (m *wireMessage) request() *Request {
	return &Request{JSONRPC: m.JSONRPC, ID: m.ID, Method: m.Method, Params: m.Params}
}

// notification converts m to a Notification. Call it only after kind
// reports kindNotification.
func (m *wireMessage) notification() *Notification {
	return &Notification{JSONRPC: m.JSONRPC, Method: m.Method, Params: m.Params}
}

// response converts m to a Response. Call it only after kind reports
// kindResponse.
func (m *wireMessage) response() *Response {
	return &Response{JSONRPC: m.JSONRPC, ID: m.ID, Result: m.Result, Error: m.Error}
}

// errNotJSONRPC reports a payload that is neither a JSON object nor a
// non-empty JSON array.
var errNotJSONRPC = errors.New("payload is not a JSON-RPC object or batch")

// decodeMessages parses one JSON-RPC object or a non-empty batch array,
// which 2025-03-26 servers may send on stdio and SSE. Leading/trailing
// whitespace is ignored. Any other shape, an
// empty array, or invalid JSON returns an error (callers decide whether
// that is skip-and-log (stdio) or errProtocol (HTTP)).
//
// A batch element that is not a well-formed message object is kept as a
// zero wireMessage, which classifies as kindInvalid, so one bad element
// never hides the valid ones next to it.
//
// A batch is decoded element by element and decoding stops after
// maxInboundBatch+1 elements, so the result is never longer than that:
// callers refuse it with batchTooLong without having paid for the rest.
// A 32 MiB array of tiny elements would otherwise cost gigabytes.
func decodeMessages(b []byte) ([]wireMessage, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, errNotJSONRPC
	}
	switch b[0] {
	case '{':
		var m wireMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("decode message: %w", err)
		}
		return []wireMessage{m}, nil
	case '[':
		return decodeBatch(b)
	default:
		return nil, errNotJSONRPC
	}
}

// decodeBatch decodes the JSON array b, stopping after
// maxInboundBatch+1 elements (see decodeMessages).
func decodeBatch(b []byte) ([]wireMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // the opening '['
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	var out []wireMessage
	for dec.More() {
		if len(out) > maxInboundBatch {
			return out, nil // over the limit: the caller refuses it
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode batch: %w", err)
		}
		var m wireMessage
		if raw = bytes.TrimSpace(raw); len(raw) > 0 && raw[0] == '{' {
			if err := json.Unmarshal(raw, &m); err != nil {
				m = wireMessage{}
			}
		}
		out = append(out, m)
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode batch: trailing data after the array")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: empty batch", errNotJSONRPC)
	}
	return out, nil
}

// hasID reports whether raw is a present, non-null id.
func hasID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// intID encodes an outbound integer id.
func intID(n int64) json.RawMessage {
	return strconv.AppendInt(nil, n, 10)
}

// idInt decodes a JSON integer id for pending-call lookup. Strings,
// floats, null and absent ids return false (they never match a pending
// call because Thane only ever sends integers).
func idInt(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	// JSON integers are an optional '-' followed by digits; reject the
	// '+' sign that strconv would otherwise accept.
	digits := raw
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// marshalParams marshals request/notification params; nil returns nil
// (so the "params" key is omitted). A value that marshals to JSON null,
// such as a nil map, is treated the same way, because JSON-RPC params
// must be an object or an array when present.
func marshalParams(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	if bytes.Equal(b, []byte("null")) {
		return nil, nil
	}
	return b, nil
}
