package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Error classes. Transports and the client wrap these with %w; callers
// classify with errors.Is / errors.As only, never by message text.
var (
	errNotReady       = errors.New("mcp: server not connected")
	errUnreachable    = errors.New("mcp: server unreachable")
	errConnectionLost = errors.New("mcp: connection lost before the response arrived; outcome unknown")
	errSessionExpired = errors.New("mcp: session expired")
	errProtocol       = errors.New("mcp: protocol violation")
	errInputRequired  = errors.New("mcp: server requires interactive input")
	errToolGone       = errors.New("mcp: tool no longer offered by the server")
	errIncompatible   = errors.New("mcp: no mutually supported protocol version")
)

// httpStatusError reports a non-2xx HTTP response. RPC is set when the
// body parsed as a single JSON-RPC response carrying an error object.
// Body is capped at 1 KiB and is for logs only.
type httpStatusError struct {
	Status          int
	RPC             *RPCError
	WWWAuthenticate string
	Body            string
}

// Error implements error (operator text; never shown to the model).
func (e *httpStatusError) Error() string {
	if e.RPC != nil {
		return fmt.Sprintf("mcp: http status %d: %v", e.Status, e.RPC)
	}
	return fmt.Sprintf("mcp: http status %d", e.Status)
}

// Unwrap exposes RPC to errors.As (so a modern 400 + -32602 classifies
// as invalid params). Returns a nil interface, never a typed nil.
func (e *httpStatusError) Unwrap() error {
	if e.RPC == nil {
		return nil
	}
	return e.RPC
}

// statusClass groups HTTP status codes by what they say about a server
// (critic A5/H2): only 2xx bodies and 400/404/405 bodies are evidence of
// the protocol era; the other classes are never era verdicts.
type statusClass int

const (
	statusOther       statusClass = iota // 1xx, 3xx, other 4xx: no era verdict
	statusSuccess                        // 2xx
	statusEraEvidence                    // 400, 404, 405: the body decides the era
	statusAuth                           // 401, 403: credentials refused
	statusThaneBug                       // 406, 415: Thane's Accept/Content-Type rejected
	statusTransient                      // 408, 429, 5xx: retry later
)

// String returns a short name for logs.
func (c statusClass) String() string {
	switch c {
	case statusSuccess:
		return "success"
	case statusEraEvidence:
		return "era-evidence"
	case statusAuth:
		return "auth"
	case statusThaneBug:
		return "client-bug"
	case statusTransient:
		return "transient"
	default:
		return "other"
	}
}

// classifyStatus maps an HTTP status code to its statusClass.
func classifyStatus(code int) statusClass {
	switch {
	case code >= 200 && code <= 299:
		return statusSuccess
	case code == 400, code == 404, code == 405:
		return statusEraEvidence
	case code == 401, code == 403:
		return statusAuth
	case code == 406, code == 415:
		return statusThaneBug
	case code == 408, code == 429, code >= 500 && code <= 599:
		return statusTransient
	default:
		return statusOther
	}
}

// serverTextCapBytes caps server-authored text quoted inside a
// model-facing error (isError text, JSON-RPC error messages).
const serverTextCapBytes = 2048

// Fixed model-facing wording (DESIGN-AMENDMENTS AM-34). Thane-authored
// text never carries a URL, host, header value or HTTP body.
const (
	reasonToolGone    = "its MCP server no longer offers this tool; use another tool"
	reasonUnreachable = "its MCP server is not reachable right now; Thane keeps retrying the connection in the background. Use another tool, or try this one again later"

	textInputRequired = "This MCP tool needs interactive input from a person, which Thane cannot provide through tool calls. Use another tool, or ask the user to do this step directly."
	textAuth          = "The MCP server refused Thane's credentials. Only the operator can fix this; use another tool, or tell the user this integration needs its credentials updated."
	textTransient     = "The MCP server reported a temporary failure. Use another tool, or try this one again later."
	textConnLost      = "The connection to the MCP server broke during the call, so the outcome is unknown. Thane is reconnecting in the background. If this tool changes state, check the current state before calling it again."
	textDeadline      = "The MCP server did not answer before the time limit, so the outcome is unknown. If this tool changes state, check the current state before calling it again; otherwise narrow the request or use another tool."
	textCanceled      = "The MCP tool call was cancelled before it finished, so the outcome is unknown. If this tool changes state, check the current state before calling it again."
	textUnusable      = "The MCP server sent a response Thane could not use. Use another tool; the operator can see the details in Thane's logs."

	leadInvalidParams = "The MCP server rejected the arguments."
	tailInvalidParams = "Change the arguments to match the tool's parameters and call again."
	leadRPCError      = "The MCP server returned an error."
	tailRPCError      = "Change the request, or use another tool."
	leadToolError     = "The MCP tool reported a failure."
	tailToolError     = "Adjust the arguments based on that message, or use another tool."

	serverBlockOpen  = "\n--- server message ---\n"
	serverBlockClose = "\n--- end server message ---\n"
	serverTextMore   = " …[truncated]"
)

// modelFacingCallError maps any error from serverClient.callTool to the
// only two shapes a bridged handler may return: tools.ErrUnavailable
// (fixed Reason) or *promptfmt.ModelFacingError (fixed words, plus
// framed and capped server-authored text where the class allows it).
// Err keeps the original error for logs. Classes are checked in the
// AM-34 order; the first match wins. A nil err returns nil.
func modelFacingCallError(tool string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errToolGone) {
		return tools.ErrUnavailable{Tool: tool, Reason: reasonToolGone}
	}
	if errors.Is(err, errNotReady) || errors.Is(err, errUnreachable) || errors.Is(err, errSessionExpired) {
		return tools.ErrUnavailable{Tool: tool, Reason: reasonUnreachable}
	}
	if errors.Is(err, errInputRequired) {
		return modelFacing(textInputRequired, err)
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch classifyStatus(statusErr.Status) {
		case statusAuth:
			return modelFacing(textAuth, err)
		case statusTransient:
			return modelFacing(textTransient, err)
		}
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		if rpcErr.Code == codeInvalidParams {
			return modelFacing(framedServerText(leadInvalidParams, rpcErr.Message, tailInvalidParams), err)
		}
		return modelFacing(framedServerText(leadRPCError, rpcErr.Message, tailRPCError), err)
	}
	switch {
	case errors.Is(err, errConnectionLost):
		return modelFacing(textConnLost, err)
	case errors.Is(err, context.DeadlineExceeded):
		return modelFacing(textDeadline, err)
	case errors.Is(err, context.Canceled):
		return modelFacing(textCanceled, err)
	}
	return modelFacing(textUnusable, err)
}

// modelFacingToolError wraps an isError tool result's rendered text.
// The server wrote that text for the model, so it is quoted (cleaned
// and capped) inside a fixed frame. Err carries a capped copy for logs.
func modelFacingToolError(tool, text string) error {
	return modelFacing(
		framedServerText(leadToolError, text, tailToolError),
		fmt.Errorf("mcp: tool %s reported an error: %s", tool, cleanServerText(text)),
	)
}

func modelFacing(text string, err error) error {
	return &promptfmt.ModelFacingError{Text: text, Err: err}
}

// framedServerText puts server-authored text between fixed Thane words,
// inside an explicit boundary so it cannot read as Thane's instructions.
// Empty server text leaves only the fixed words.
func framedServerText(lead, server, tail string) string {
	server = cleanServerText(server)
	if server == "" {
		return lead + " " + tail
	}
	return lead + serverBlockOpen + server + serverBlockClose + tail
}

// cleanServerText replaces control characters other than newline and
// tab with a space, trims surrounding whitespace, and caps the result at
// serverTextCapBytes (rune-safe, marked when cut).
func cleanServerText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) <= serverTextCapBytes {
		return s
	}
	return truncateUTF8(s, serverTextCapBytes-len(serverTextMore)) + serverTextMore
}
