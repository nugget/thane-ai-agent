package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestErrors_HTTPStatusError(t *testing.T) {
	plain := &httpStatusError{Status: 503, Body: "upstream down"}
	if got := plain.Error(); got != "mcp: http status 503" {
		t.Errorf("Error() = %q", got)
	}
	if plain.Unwrap() != nil {
		t.Errorf("Unwrap() without RPC = %#v, want a nil interface", plain.Unwrap())
	}
	var rpc *RPCError
	if errors.As(plain, &rpc) {
		t.Error("errors.As found an RPCError in a status error without one")
	}

	withRPC := &httpStatusError{Status: 400, RPC: &RPCError{Code: codeInvalidParams, Message: "bad region"}}
	if got := withRPC.Error(); got != "mcp: http status 400: jsonrpc error -32602: bad region" {
		t.Errorf("Error() = %q", got)
	}
	wrapped := fmt.Errorf("call: %w", withRPC)
	if !errors.As(wrapped, &rpc) || rpc.Code != codeInvalidParams {
		t.Errorf("errors.As through Unwrap = %v", rpc)
	}
	var se *httpStatusError
	if !errors.As(wrapped, &se) || se.Status != 400 {
		t.Errorf("errors.As(*httpStatusError) = %v", se)
	}
}

func TestErrors_ClassifyStatus(t *testing.T) {
	tests := []struct {
		code int
		want statusClass
	}{
		{0, statusOther},
		{100, statusOther},
		{200, statusSuccess},
		{201, statusSuccess},
		{202, statusSuccess},
		{204, statusSuccess},
		{299, statusSuccess},
		{301, statusOther},
		{302, statusOther},
		{303, statusOther},
		{307, statusOther},
		{308, statusOther},
		{400, statusEraEvidence},
		{401, statusAuth},
		{402, statusOther},
		{403, statusAuth},
		{404, statusEraEvidence},
		{405, statusEraEvidence},
		{406, statusThaneBug},
		{407, statusOther},
		{408, statusTransient},
		{409, statusOther},
		{410, statusOther},
		{413, statusOther},
		{415, statusThaneBug},
		{422, statusOther},
		{429, statusTransient},
		{499, statusOther},
		{500, statusTransient},
		{502, statusTransient},
		{503, statusTransient},
		{504, statusTransient},
		{599, statusTransient},
		{600, statusOther},
	}
	for _, tt := range tests {
		if got := classifyStatus(tt.code); got != tt.want {
			t.Errorf("classifyStatus(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestErrors_StatusClassString(t *testing.T) {
	tests := map[statusClass]string{
		statusOther:       "other",
		statusSuccess:     "success",
		statusEraEvidence: "era-evidence",
		statusAuth:        "auth",
		statusThaneBug:    "client-bug",
		statusTransient:   "transient",
		statusClass(99):   "other",
	}
	for c, want := range tests {
		if got := c.String(); got != want {
			t.Errorf("statusClass(%d).String() = %q, want %q", int(c), got, want)
		}
	}
}

// coreTModelText returns what the model reads for a bridged handler
// error, and whether it is the ErrUnavailable shape.
func coreTModelText(t *testing.T, err error) (string, bool) {
	t.Helper()
	var unavailable tools.ErrUnavailable
	if errors.As(err, &unavailable) {
		return unavailable.Error(), true
	}
	text, ok := promptfmt.ModelFacingText(err)
	if !ok {
		t.Fatalf("error %T %v is neither tools.ErrUnavailable nor a ModelFacingError", err, err)
	}
	return text, false
}

func TestErrors_ModelFacingCallError(t *testing.T) {
	const tool = "mcp_weather_get"
	rpcInvalid := &RPCError{Code: codeInvalidParams, Message: "region must be one of us, eu"}
	rpcOther := &RPCError{Code: -32603, Message: "database offline"}

	tests := []struct {
		name        string
		err         error
		unavailable string // expected ErrUnavailable Reason ("" = ModelFacingError)
		text        string // expected exact model text (ModelFacingError)
	}{
		{name: "tool gone", err: errToolGone, unavailable: reasonToolGone},
		{name: "tool gone beats not ready", err: fmt.Errorf("%w: %w", errNotReady, errToolGone), unavailable: reasonToolGone},
		{name: "not ready", err: fmt.Errorf("%w: last error: dial", errNotReady), unavailable: reasonUnreachable},
		{name: "not ready beats auth", err: fmt.Errorf("%w: %w", errNotReady, &httpStatusError{Status: 401}), unavailable: reasonUnreachable},
		{name: "unreachable", err: fmt.Errorf("%w: connection refused", errUnreachable), unavailable: reasonUnreachable},
		{name: "session expired", err: fmt.Errorf("%w: %w", errSessionExpired, &httpStatusError{Status: 404}), unavailable: reasonUnreachable},
		{name: "input required", err: errInputRequired, text: textInputRequired},
		{name: "401", err: &httpStatusError{Status: 401, WWWAuthenticate: `Bearer error="invalid_token"`}, text: textAuth},
		{name: "403 with rpc invalid params", err: &httpStatusError{Status: 403, RPC: rpcInvalid}, text: textAuth},
		{name: "408", err: &httpStatusError{Status: 408}, text: textTransient},
		{name: "429", err: &httpStatusError{Status: 429}, text: textTransient},
		{name: "500", err: &httpStatusError{Status: 500}, text: textTransient},
		{name: "503 wrapped", err: fmt.Errorf("post: %w", &httpStatusError{Status: 503}), text: textTransient},
		{
			name: "invalid params",
			err:  fmt.Errorf("tools/call: %w", rpcInvalid),
			text: leadInvalidParams + serverBlockOpen + rpcInvalid.Message + serverBlockClose + tailInvalidParams,
		},
		{
			name: "400 carrying invalid params",
			err:  &httpStatusError{Status: 400, RPC: rpcInvalid},
			text: leadInvalidParams + serverBlockOpen + rpcInvalid.Message + serverBlockClose + tailInvalidParams,
		},
		{
			name: "other rpc error",
			err:  rpcOther,
			text: leadRPCError + serverBlockOpen + rpcOther.Message + serverBlockClose + tailRPCError,
		},
		{
			name: "rpc error with empty message",
			err:  &RPCError{Code: -32000},
			text: leadRPCError + " " + tailRPCError,
		},
		{name: "connection lost", err: fmt.Errorf("%w: eof", errConnectionLost), text: textConnLost},
		{name: "connection lost beats deadline", err: fmt.Errorf("%w: %w", errConnectionLost, context.DeadlineExceeded), text: textConnLost},
		{name: "deadline", err: fmt.Errorf("tools/call: %w", context.DeadlineExceeded), text: textDeadline},
		{name: "cancelled", err: context.Canceled, text: textCanceled},
		{name: "protocol", err: fmt.Errorf("%w: unknown resultType %q", errProtocol, "task"), text: textUnusable},
		{name: "incompatible", err: errIncompatible, text: textUnusable},
		{name: "400 without rpc", err: &httpStatusError{Status: 400}, text: textUnusable},
		{name: "418", err: &httpStatusError{Status: 418}, text: textUnusable},
		{name: "unknown", err: errors.New("something odd"), text: textUnusable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelFacingCallError(tool, tt.err)
			if tt.unavailable != "" {
				var u tools.ErrUnavailable
				if !errors.As(got, &u) {
					t.Fatalf("got %T %v, want tools.ErrUnavailable", got, got)
				}
				if u.Tool != tool || u.Reason != tt.unavailable {
					t.Errorf("ErrUnavailable = %+v, want reason %q", u, tt.unavailable)
				}
				return
			}
			var mfe *promptfmt.ModelFacingError
			if !errors.As(got, &mfe) {
				t.Fatalf("got %T %v, want *promptfmt.ModelFacingError", got, got)
			}
			if mfe.Text != tt.text {
				t.Errorf("Text =\n%q\nwant\n%q", mfe.Text, tt.text)
			}
			if mfe.Err != tt.err {
				t.Errorf("Err = %v, want the original error", mfe.Err)
			}
			if !errors.Is(got, tt.err) {
				t.Error("errors.Is(result, original) = false")
			}
		})
	}

	if modelFacingCallError(tool, nil) != nil {
		t.Error("modelFacingCallError(nil) != nil")
	}
}

// TestErrors_ModelTextNeverLeaksTransportDetails runs every class with
// operator-only details (URL, host, header values, HTTP body) in the
// error and asserts none of it reaches the model.
func TestErrors_ModelTextNeverLeaksTransportDetails(t *testing.T) {
	const (
		url    = "https://mcp.internal.example:8443/v1/mcp?token=s3cr3t"
		host   = "mcp.internal.example"
		ip     = "10.20.30.40"
		header = "Bearer eyJhbGciOiJIUzI1NiJ9.secret"
		body   = "<html>nginx stack trace SECRET-BODY</html>"
	)
	leaky := fmt.Sprintf("POST %s (%s, %s): Authorization: %s; body %s", url, host, ip, header, body)
	status := func(code int, rpc *RPCError) error {
		return fmt.Errorf("%w; %s", &httpStatusError{Status: code, RPC: rpc, WWWAuthenticate: header, Body: body}, leaky)
	}
	cleanRPC := func(code int) *RPCError { return &RPCError{Code: code, Message: "server says no"} }

	errs := []error{
		fmt.Errorf("%w: %s", errToolGone, leaky),
		fmt.Errorf("%w: %s", errNotReady, leaky),
		fmt.Errorf("%w: %s", errUnreachable, leaky),
		fmt.Errorf("%w: %w", errSessionExpired, status(404, nil)),
		fmt.Errorf("%w: %s", errInputRequired, leaky),
		status(401, nil),
		status(403, cleanRPC(codeInvalidParams)),
		status(408, nil),
		status(429, nil),
		status(500, nil),
		status(502, cleanRPC(-32603)),
		status(400, cleanRPC(codeInvalidParams)),
		status(400, cleanRPC(codeHeaderMismatch)),
		status(400, nil),
		status(404, nil),
		status(418, nil),
		fmt.Errorf("%s: %w", leaky, cleanRPC(codeInvalidParams)),
		fmt.Errorf("%s: %w", leaky, cleanRPC(-32001)),
		fmt.Errorf("%w: %s", errConnectionLost, leaky),
		fmt.Errorf("%s: %w", leaky, context.DeadlineExceeded),
		fmt.Errorf("%s: %w", leaky, context.Canceled),
		fmt.Errorf("%w: %s", errProtocol, leaky),
		fmt.Errorf("%w: %s", errIncompatible, leaky),
		errors.New(leaky),
	}
	for i, err := range errs {
		text, _ := coreTModelText(t, modelFacingCallError("mcp_srv_tool", err))
		for _, secret := range []string{"http://", "https://", host, ip, "Bearer", "eyJ", "SECRET-BODY", "nginx", "token=", "8443"} {
			if strings.Contains(text, secret) {
				t.Errorf("case %d (%v): model text leaks %q:\n%s", i, err, secret, text)
			}
		}
		if text == "" {
			t.Errorf("case %d: empty model text", i)
		}
	}
}

func TestErrors_ModelFacingToolError(t *testing.T) {
	err := modelFacingToolError("mcp_gh_create_issue", "Repository not found: acme/widgets")
	var mfe *promptfmt.ModelFacingError
	if !errors.As(err, &mfe) {
		t.Fatalf("got %T, want *promptfmt.ModelFacingError", err)
	}
	want := leadToolError + serverBlockOpen + "Repository not found: acme/widgets" + serverBlockClose + tailToolError
	if mfe.Text != want {
		t.Errorf("Text =\n%q\nwant\n%q", mfe.Text, want)
	}
	if !strings.Contains(mfe.Err.Error(), "mcp_gh_create_issue") || !strings.Contains(mfe.Err.Error(), "Repository not found") {
		t.Errorf("Err = %q, want tool name and message for logs", mfe.Err)
	}

	empty := modelFacingToolError("t", " \x00\n ")
	if text, _ := promptfmt.ModelFacingText(empty); text != leadToolError+" "+tailToolError {
		t.Errorf("empty server text: %q", text)
	}

	long := modelFacingToolError("t", strings.Repeat("é", 3000))
	text, _ := promptfmt.ModelFacingText(long)
	inner := strings.TrimSuffix(strings.TrimPrefix(text, leadToolError+serverBlockOpen), serverBlockClose+tailToolError)
	if len(inner) > serverTextCapBytes || !strings.HasSuffix(inner, serverTextMore) || !utf8.ValidString(inner) {
		t.Errorf("capped server text: %d bytes, valid=%v, suffix=%q", len(inner), utf8.ValidString(inner), inner[len(inner)-20:])
	}
	if len(mfe.Err.Error()) > serverTextCapBytes+100 {
		t.Errorf("log error is not capped: %d bytes", len(mfe.Err.Error()))
	}
}

func TestErrors_CleanServerText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{name: "plain", in: "hello", want: "hello"},
		{name: "keeps newline and tab", in: "a\nb\tc", want: "a\nb\tc"},
		{name: "carriage return", in: "a\r\nb", want: "a \nb"},
		{name: "nul and escape", in: "a\x00b\x1b[31mc", want: "a b [31mc"},
		{name: "delete and C1", in: "a\x7fb\u0085c", want: "a b c"},
		{name: "trims", in: "  \n hi \t\n", want: "hi"},
		{name: "only controls", in: "\x00\x01\x02", want: ""},
		{name: "unicode kept", in: "日本語 ✓", want: "日本語 ✓"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanServerText(tt.in); got != tt.want {
				t.Errorf("cleanServerText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	exact := strings.Repeat("x", serverTextCapBytes)
	if got := cleanServerText(exact); got != exact {
		t.Errorf("text at the cap was cut to %d bytes", len(got))
	}
	over := cleanServerText(exact + "y")
	if len(over) != serverTextCapBytes || !strings.HasSuffix(over, serverTextMore) {
		t.Errorf("over cap: %d bytes, suffix ok=%v", len(over), strings.HasSuffix(over, serverTextMore))
	}
	multi := cleanServerText(strings.Repeat("世", 1000))
	if len(multi) > serverTextCapBytes || !utf8.ValidString(multi) {
		t.Errorf("multibyte cap: %d bytes, valid=%v", len(multi), utf8.ValidString(multi))
	}
}
