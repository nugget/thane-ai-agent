package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// clientTAuthRefusal is a 401 as a server sends it: the challenge and the
// body say why, and only the operator log may show them.
func clientTAuthRefusal() *httpStatusError {
	return &httpStatusError{
		Status:          401,
		WWWAuthenticate: `Bearer error="invalid_token", error_description="token expired"`,
		Body:            "BODY-MARKER: rotate the key\x00 at the admin console",
	}
}

func TestErrors_HTTPStatusLogArgs(t *testing.T) {
	if args := httpStatusLogArgs(errors.New("no status")); args != nil {
		t.Errorf("args without a status error = %v, want nil", args)
	}
	args := httpStatusLogArgs(fmt.Errorf("server/discover: %w", clientTAuthRefusal()))
	want := []any{
		"http_status", 401,
		"www_authenticate", `Bearer error="invalid_token", error_description="token expired"`,
		"http_body", "BODY-MARKER: rotate the key  at the admin console",
	}
	if !slices.Equal(args, want) {
		t.Errorf("args = %q\nwant   %q", args, want)
	}
	if args := httpStatusLogArgs(&httpStatusError{Status: 503}); !slices.Equal(args, []any{"http_status", 503}) {
		t.Errorf("args for a bare status = %v", args)
	}
	long := httpStatusLogArgs(&httpStatusError{Status: 400, Body: strings.Repeat("x", 4*httpLogBodyCap)})
	if body := long[len(long)-1].(string); len(body) > httpLogBodyCap {
		t.Errorf("body attribute is %d bytes, want at most %d", len(body), httpLogBodyCap)
	}
	// The details stay out of Error(), which health views and the model's
	// error paths render.
	if text := clientTAuthRefusal().Error(); strings.Contains(text, "token expired") || strings.Contains(text, "BODY-MARKER") {
		t.Errorf("Error() = %q carries log-only details", text)
	}
}

func TestErrors_ClientClosedIsShuttingDown(t *testing.T) {
	err := modelFacingCallError("mcp_s_echo", notReadyErr(errClientClosed))
	var unavailable tools.ErrUnavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != reasonShuttingDown {
		t.Fatalf("closed client = %v, want ErrUnavailable(%q)", err, reasonShuttingDown)
	}
	if strings.Contains(unavailable.Error(), "retrying") {
		t.Errorf("shutdown text promises a retry: %q", unavailable.Error())
	}
}

// TestBridge_CallStatusLoggedForTheOperator: a tool call that fails with
// an HTTP status logs the status details (never shown to the model) once
// per status class: WARN when the operator must act (refused
// credentials, or a response the model was told to look up in the
// logs), Debug otherwise.
func TestBridge_CallStatusLoggedForTheOperator(t *testing.T) {
	t.Parallel()
	answers := map[string]error{
		"auth":      clientTAuthRefusal(),
		"plain400":  &httpStatusError{Status: 400, Body: "BODY-400: payload too odd"},
		"transient": &httpStatusError{Status: 503, Body: "BODY-503"},
	}
	routes := clientTLegacyRoutes("", clientTTool("auth"), clientTTool("plain400"), clientTTool("transient"))
	routes[methodToolsCall] = func(_ context.Context, _ *clientTFakeTransport, req *Request, _ callInfo) (*Response, error) {
		for name, err := range answers {
			if strings.Contains(string(req.Params), `"`+name+`"`) {
				return nil, err
			}
		}
		return nil, errors.New("unexpected tool")
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, protocolLegacy))
	logs := &coreTLogBuffer{}
	b := newServerBridge(ServerConfig{Name: "fake"}, c, logs.logger())
	clientTStart(t, c)
	clientTReadyConn(t, c)
	handlers := map[string]func(context.Context, map[string]any) (string, error){}
	for _, bt := range b.bridge(wireTDefs("auth", "plain400", "transient")) {
		handlers[bt.raw] = bt.tool.Handler
	}
	call := func(name string) error {
		t.Helper()
		_, err := handlers[name](t.Context(), nil)
		if err == nil {
			t.Fatalf("%s succeeded", name)
		}
		return err
	}

	for range 2 {
		call("auth")
		err := call("plain400")
		var mfe *promptfmt.ModelFacingError
		if !errors.As(err, &mfe) || mfe.Text != textUnusable {
			t.Fatalf("plain 400 = %v, want textUnusable", err)
		}
		call("transient")
	}
	out := logs.String()
	const msg = `msg="mcp: tool call failed with an HTTP error status"`
	for _, tc := range []struct {
		marker      string
		warn, debug int
	}{
		{"token expired", 1, 1},
		{"BODY-400", 1, 1},
		{"BODY-503", 0, 2},
	} {
		var warn, debug int
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, msg) || !strings.Contains(line, tc.marker) {
				continue
			}
			switch {
			case strings.Contains(line, "level=WARN"):
				warn++
			case strings.Contains(line, "level=DEBUG"):
				debug++
			}
		}
		if warn != tc.warn || debug != tc.debug {
			t.Errorf("%s: WARN %d, Debug %d; want %d, %d\n%s", tc.marker, warn, debug, tc.warn, tc.debug, out)
		}
	}
	if !strings.Contains(out, "tool=mcp_fake_auth") {
		t.Errorf("status log does not name the Thane tool:\n%s", out)
	}
}
