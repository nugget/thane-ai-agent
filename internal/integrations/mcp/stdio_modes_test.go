package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests pin the behaviour of the "mcp-legacy" and "mcp-modern"
// helper modes that client and end-to-end tests run real Clients against.

// stdioTRPCCode returns the error code of a call that must reach the
// server, or 0 for a result.
func stdioTRPCCode(t *testing.T, tr *stdioTransport, method string, params any) int {
	t.Helper()
	resp, err := stdioTCall(t, tr, method, params, callInfo{})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if resp.Error != nil {
		return resp.Error.Code
	}
	return 0
}

// stdioTListAll pages through tools/list and returns the tool names.
func stdioTListAll(t *testing.T, tr *stdioTransport, meta map[string]any) []string {
	t.Helper()
	var names []string
	var cursor *string
	for range 5 {
		params := map[string]any{}
		if meta != nil {
			params["_meta"] = meta
		}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		var res toolsListResult
		stdioTMust(t, tr, methodToolsList, params, &res)
		for _, raw := range res.Tools {
			var def toolDefinition
			if err := json.Unmarshal(raw, &def); err != nil {
				t.Fatalf("tool %s: %v", raw, err)
			}
			names = append(names, def.Name)
		}
		if meta != nil && res.ResultType != resultTypeComplete {
			t.Errorf("modern tools/list resultType = %q", res.ResultType)
		}
		if res.NextCursor == nil {
			return names
		}
		cursor = res.NextCursor
	}
	t.Fatal("tools/list never ended")
	return nil
}

// stdioTToolText calls a tool and returns its rendered result.
func stdioTToolText(t *testing.T, tr *stdioTransport, params map[string]any) string {
	t.Helper()
	var res callToolResult
	stdioTMust(t, tr, methodToolsCall, params, &res)
	return renderToolResult(&res)
}

func TestStdio_HelperModeLegacy(t *testing.T) {
	tr := stdioTStart(t, stdioTHelper(t, "mcp-legacy"), transportHooks{})

	if code := stdioTRPCCode(t, tr, methodDiscover, map[string]any{"_meta": modernMeta()}); code != codeMethodNotFound {
		t.Errorf("server/discover code = %d, want -32601", code)
	}
	if code := stdioTRPCCode(t, tr, methodToolsList, nil); code != codeInvalidRequest {
		t.Errorf("tools/list before initialize code = %d, want -32600", code)
	}
	var init initializeResult
	stdioTMust(t, tr, methodInitialize, map[string]any{
		"protocolVersion": handshakeOfferVersion, "capabilities": map[string]any{}, "clientInfo": thaneClientInfo(),
	}, &init)
	if init.ProtocolVersion != handshakeOfferVersion || init.ServerInfo.Name == "" || init.Capabilities.Tools == nil {
		t.Errorf("initialize = %+v", init)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}
	if code := stdioTRPCCode(t, tr, methodPing, nil); code != 0 {
		t.Errorf("ping code = %d, want a result", code)
	}
	if got := strings.Join(stdioTListAll(t, tr, nil), ","); got != "echo,sleep" {
		t.Errorf("tools = %s, want echo,sleep", got)
	}
	if got := stdioTToolText(t, tr, map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}}); got != "hi" {
		t.Errorf("echo = %q", got)
	}
	if code := stdioTRPCCode(t, tr, methodToolsList, map[string]any{"_meta": modernMeta()}); code != codeInvalidRequest {
		t.Errorf("modern _meta on legacy code = %d, want -32600", code)
	}
	if code := stdioTRPCCode(t, tr, methodToolsCall, map[string]any{"name": "nope", "arguments": map[string]any{}}); code != codeInvalidParams {
		t.Errorf("unknown tool code = %d, want -32602", code)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := tr.Call(ctx, &Request{Method: methodToolsCall, Params: json.RawMessage(`{"name":"sleep","arguments":{"ms":5000}}`)}, callInfo{cancellable: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled sleep: %v", err)
	}
	if got := stdioTToolText(t, tr, map[string]any{"name": "sleep", "arguments": map[string]any{"ms": 1}}); got != "slept 1ms" {
		t.Errorf("sleep = %q", got)
	}
}

func TestStdio_HelperModeModern(t *testing.T) {
	tr := stdioTStart(t, stdioTHelper(t, "mcp-modern"), transportHooks{})
	meta := modernMeta()

	resp, err := stdioTCall(t, tr, methodInitialize, map[string]any{"protocolVersion": handshakeOfferVersion}, callInfo{})
	if err != nil || resp.Error == nil || resp.Error.Code != codeUnsupportedProtocolVersion {
		t.Fatalf("initialize = (%+v, %v), want -32022", resp, err)
	}
	var data unsupportedVersionData
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil || len(data.Supported) != 1 || data.Supported[0] != modernProtocolVersion {
		t.Errorf("-32022 data = %s", resp.Error.Data)
	}
	if code := stdioTRPCCode(t, tr, methodDiscover, nil); code != codeInvalidParams {
		t.Errorf("discover without _meta code = %d, want -32602", code)
	}
	other := modernMeta()
	other[metaProtocolVersion] = "2099-01-01"
	if code := stdioTRPCCode(t, tr, methodToolsList, map[string]any{"_meta": other}); code != codeUnsupportedProtocolVersion {
		t.Errorf("unknown modern version code = %d, want -32022", code)
	}

	var disc discoverResult
	stdioTMust(t, tr, methodDiscover, map[string]any{"_meta": meta}, &disc)
	info, ok := disc.serverInfo()
	if disc.Capabilities == nil || len(disc.SupportedVersions) != 1 || disc.SupportedVersions[0] != modernProtocolVersion || !ok || info.Name == "" {
		t.Errorf("discover = %+v", disc)
	}
	if code := stdioTRPCCode(t, tr, methodPing, map[string]any{"_meta": meta}); code != codeMethodNotFound {
		t.Errorf("ping code = %d, want -32601", code)
	}
	if got := strings.Join(stdioTListAll(t, tr, meta), ","); got != "echo,sleep" {
		t.Errorf("tools = %s, want echo,sleep", got)
	}
	if got := stdioTToolText(t, tr, map[string]any{"_meta": meta, "name": "echo", "arguments": map[string]any{"text": "hi"}}); got != "hi" {
		t.Errorf("echo = %q", got)
	}
}
