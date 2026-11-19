package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// toolSet is one published tools/list result. Immutable once published.
type toolSet struct {
	defs    []toolDefinition         // server order, deduped; never nil
	index   map[string]int           // name → position in defs
	headers map[string][]headerParam // HTTP: validated x-mcp-header params per tool
	fp      []byte                   // fingerprint: compact raw JSON of defs
	ttl     time.Duration            // modern ttlMs (minimum across pages); 0 for legacy
}

// fetchTools runs a full paginated tools/list. Pagination stops
// only when nextCursor is absent or null ("" is a valid cursor), after
// maxListPages pages, or when a cursor repeats. A -32601 to the first
// page means the server offers no tools.
func (c *serverClient) fetchTools(ctx context.Context, conn *clientConn) (*toolSet, error) {
	b := newToolSetBuilder(c, c.cfg.transport == transportHTTP)
	seenCursors := map[string]struct{}{}
	minTTL := int64(-1)
	var cursor *string
	for page := 0; ; page++ {
		if page == maxListPages {
			c.logger.Warn("mcp: tools/list kept paginating; using the tools listed so far",
				"pages", maxListPages)
			break
		}
		var fields map[string]any
		if cursor != nil {
			fields = map[string]any{"cursor": *cursor}
		}
		resp, err := conn.call(ctx, methodToolsList, fields, conn.frame(true))
		var rpcErr *RPCError
		if page == 0 && rpcErrorOf(resp, err, &rpcErr) && rpcErr.Code == codeMethodNotFound {
			c.logger.Info("mcp: server does not implement tools/list; it offers no tools")
			return b.finish(conn, 0), nil
		}
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		var res toolsListResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			return nil, fmt.Errorf("%w: malformed tools/list result: %w", errProtocol, err)
		}
		if res.ResultType != "" && res.ResultType != resultTypeComplete {
			return nil, fmt.Errorf("%w: tools/list returned resultType %q", errProtocol, logText(res.ResultType))
		}
		ttl := int64(0)
		if res.TTLMs != nil && *res.TTLMs > 0 {
			ttl = *res.TTLMs
		}
		if minTTL < 0 || ttl < minTTL {
			minTTL = ttl
		}
		for _, raw := range res.Tools {
			b.add(raw)
		}
		if res.NextCursor == nil {
			break
		}
		if _, dup := seenCursors[*res.NextCursor]; dup {
			c.logger.Warn("mcp: tools/list repeated a cursor; using the tools listed so far")
			break
		}
		seenCursors[*res.NextCursor] = struct{}{}
		cursor = res.NextCursor
	}
	return b.finish(conn, max(minTTL, 0)), nil
}

// rpcErrorOf finds the JSON-RPC error answering a request: resp.Error,
// or the one carried by an HTTP error status.
func rpcErrorOf(resp *Response, err error, target **RPCError) bool {
	if err != nil {
		return errors.As(err, target)
	}
	if resp != nil && resp.Error != nil {
		*target = resp.Error
		return true
	}
	return false
}

// toolSetBuilder accumulates listed tools: each raw definition decodes
// on its own (a malformed one is skipped with a WARN once per name),
// the first of a duplicated name wins, and on HTTP a tool with an
// invalid x-mcp-header annotation is dropped (streamable-http MUST).
type toolSetBuilder struct {
	c       *serverClient
	http    bool
	set     *toolSet
	raws    [][]byte
	handled map[string]struct{}
}

func newToolSetBuilder(c *serverClient, http bool) *toolSetBuilder {
	return &toolSetBuilder{
		c:       c,
		http:    http,
		set:     &toolSet{defs: []toolDefinition{}, index: map[string]int{}, headers: map[string][]headerParam{}},
		handled: map[string]struct{}{},
	}
}

func (b *toolSetBuilder) add(raw json.RawMessage) {
	var def toolDefinition
	err := json.Unmarshal(raw, &def)
	if err == nil && def.Name == "" {
		err = errors.New("missing name")
	}
	if err != nil {
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &named)
		b.c.warnOnce("malformed-tool:"+logText(named.Name), "mcp: skipping a malformed tool definition",
			"tool", logText(named.Name), "error", err)
		return
	}
	if _, dup := b.handled[def.Name]; dup {
		b.c.warnOnce("duplicate-tool:"+logText(def.Name), "mcp: server listed a tool name twice; keeping the first",
			"tool", logText(def.Name))
		return
	}
	b.handled[def.Name] = struct{}{}
	if b.http {
		params, herr := toolHeaderParams(def.InputSchema)
		if herr != nil {
			b.c.warnOnce("invalid-header:"+logText(def.Name), "mcp: dropping a tool with an invalid x-mcp-header annotation",
				"tool", logText(def.Name), "reason", herr)
			return
		}
		if len(params) > 0 {
			b.set.headers[def.Name] = params
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		compact.Reset()
		compact.Write(raw)
	}
	b.set.index[def.Name] = len(b.set.defs)
	b.set.defs = append(b.set.defs, def)
	b.raws = append(b.raws, compact.Bytes())
}

// finish seals the set. Only modern connections keep a ttl.
func (b *toolSetBuilder) finish(conn *clientConn, ttlMs int64) *toolSet {
	if conn.currentEra() == eraModern {
		b.set.ttl = time.Duration(ttlMs) * time.Millisecond
	}
	b.set.fp = bytes.Join(b.raws, []byte{'\n'})
	return b.set
}

// installLocked publishes set and reports whether the list changed. An
// unchanged list keeps the previous definitions (only the ttl moves).
// c.mu must be held.
func (c *serverClient) installLocked(set *toolSet) bool {
	now := time.Now()
	c.listedAt, c.refreshBase = now, now
	if c.tl != nil && bytes.Equal(c.tl.fp, set.fp) {
		kept := *c.tl
		kept.ttl = set.ttl
		c.tl = &kept
		return false
	}
	c.tl = set
	c.listGen++
	return true
}

// relist re-lists tools on conn, single-flight: a caller that waited
// while another listing of the same connection completed reuses it. A
// change is published and the supervisor is woken to fire
// onToolsChanged. A result for a connection that is no longer current
// is discarded.
func (c *serverClient) relist(ctx context.Context, conn *clientConn) error {
	asked := time.Now()
	select {
	case c.listSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.listSem }()

	c.mu.Lock()
	fresh := c.conn == conn && c.listedAt.After(asked)
	if !fresh {
		c.refreshBase = time.Now()
	}
	c.mu.Unlock()
	if fresh {
		return nil
	}
	set, err := c.fetchTools(ctx, conn)
	if err != nil {
		return err
	}
	c.mu.Lock()
	changed := c.conn == conn && c.installLocked(set)
	c.mu.Unlock()
	if changed {
		select {
		case c.notifyCh <- struct{}{}:
		default:
		}
	}
	return nil
}

// noteAccess is called on every Manager.Snapshot and tool call; for a
// modern connection whose list is older than max(ttl, listRefreshFloor)
// it requests an asynchronous refresh. Non-blocking, no I/O.
func (c *serverClient) noteAccess() {
	c.mu.Lock()
	stale := c.conn != nil && c.conn.currentEra() == eraModern && c.tl != nil &&
		time.Since(c.refreshBase) >= max(c.tl.ttl, c.cfg.listRefreshFloor)
	if stale {
		c.refreshBase = time.Now()
	}
	c.mu.Unlock()
	if stale {
		c.postRefresh()
	}
}

// offers reports whether name is in the known tool list (true while no
// list is known yet).
func (c *serverClient) offers(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tl == nil {
		return true
	}
	_, ok := c.tl.index[name]
	return ok
}

// callTool invokes a tool by its raw MCP name. Tool-reported
// failures return (toolResult{IsError: true}, nil). Every other failure
// returns an error classified by errors.go classes.
//
// Retries: an expired session re-initializes and retries once (the
// server rejected the request unprocessed); an HTTP HeaderMismatch
// (-32020) re-lists and retries once with fresh Mcp-Param-* headers. A
// call whose outcome is unknown (connection lost, timeout) is never
// re-issued.
func (c *serverClient) callTool(ctx context.Context, name string, args map[string]any) (toolResult, error) {
	c.noteAccess()
	conn, err := c.readyConn()
	if err != nil {
		return toolResult{}, err
	}
	if !c.offers(name) {
		return toolResult{}, fmt.Errorf("%w: %s", errToolGone, logText(name))
	}
	// WithTimeout keeps an earlier caller deadline.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.callTimeout)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	for retried := false; ; retried = true {
		res, err := c.callRounds(ctx, conn, name, args)
		if err == nil || retried {
			return res, err
		}
		switch {
		case errors.Is(err, errSessionExpired):
			c.requestReconnect(conn)
			next, werr := c.awaitReplacement(ctx, conn)
			if werr != nil {
				return toolResult{}, fmt.Errorf("%w: reconnecting failed: %w", errSessionExpired, werr)
			}
			conn = next
		case isHeaderMismatch(err):
			if lerr := c.relist(ctx, conn); lerr != nil {
				return toolResult{}, err
			}
		default:
			return res, err
		}
		if !c.offers(name) {
			return toolResult{}, fmt.Errorf("%w: %s", errToolGone, logText(name))
		}
	}
}

// callRounds sends one tools/call and follows requestState-only
// input_required continuations, up to maxInputRequiredRounds re-sends.
func (c *serverClient) callRounds(ctx context.Context, conn *clientConn, name string, args map[string]any) (toolResult, error) {
	var state *string
	for round := 0; ; round++ {
		fields := map[string]any{"name": name, "arguments": args}
		if state != nil {
			fields["requestState"] = *state
		}
		resp, err := conn.call(ctx, methodToolsCall, fields, c.callInfoFor(conn, name, args))
		if err != nil {
			if rpcErr := statusCarriedRPC(err); rpcErr != nil {
				return toolResult{}, c.callRPCError(ctx, conn, name, rpcErr, err)
			}
			return toolResult{}, fmt.Errorf("mcp: %s %s: %w", methodToolsCall, logText(name), err)
		}
		if resp.Error != nil {
			return toolResult{}, c.callRPCError(ctx, conn, name, resp.Error, resp.Error)
		}
		var r callToolResult
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			return toolResult{}, fmt.Errorf("%w: malformed tools/call result: %w", errProtocol, err)
		}
		switch r.ResultType {
		case "", resultTypeComplete:
			return toolResult{Text: renderToolResult(&r), IsError: r.IsError}, nil
		case resultTypeInputRequired:
			switch {
			case len(r.InputRequests) > 0:
				return toolResult{}, fmt.Errorf("%w: tool %s", errInputRequired, logText(name))
			case r.RequestState == nil:
				return toolResult{}, fmt.Errorf("%w: input_required result without inputRequests or requestState", errProtocol)
			case round >= maxInputRequiredRounds:
				return toolResult{}, fmt.Errorf("%w: tool %s still required input after %d continuations",
					errProtocol, logText(name), maxInputRequiredRounds)
			}
			state = r.RequestState
		default:
			// Never an empty success: Thane advertises no extension, so
			// "task" and any other non-core resultType is invalid
			// (2026-07-28 basic/index.mdx "ResultType").
			return toolResult{}, fmt.Errorf("%w: unsupported tools/call resultType %q", errProtocol, logText(r.ResultType))
		}
	}
}

// statusCarriedRPC returns the -32601 or -32602 that a non-2xx HTTP
// status carries. The 2026-07-28 Streamable HTTP transport sends
// JSON-RPC errors with a matching status (404 for an unknown method, 400
// for invalid params), and they mean the same as in a 200 body. nil for
// anything else, including an expired session (callTool retries those)
// and -32020 (callTool re-lists and retries).
func statusCarriedRPC(err error) *RPCError {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.RPC == nil || errors.Is(err, errSessionExpired) {
		return nil
	}
	if code := statusErr.RPC.Code; code != codeMethodNotFound && code != codeInvalidParams {
		return nil
	}
	return statusErr.RPC
}

// callRPCError handles a JSON-RPC error answering tools/call; cause is
// what the transport returned (the error object itself, or the HTTP
// status error carrying it), kept for the logs.
//
// A relayed decline of Thane's own (the tool asked Thane for input it
// cannot give) means the tool needs input. Otherwise -32601 and -32602
// may mean the tool list moved, so the list is refreshed and a tool that
// disappeared is reported as gone.
func (c *serverClient) callRPCError(ctx context.Context, conn *clientConn, name string, rpcErr *RPCError, cause error) error {
	if isThaneDecline(rpcErr) {
		return fmt.Errorf("%w: tool %s: the server relayed Thane's refusal: %w", errInputRequired, logText(name), cause)
	}
	if rpcErr.Code == codeMethodNotFound || rpcErr.Code == codeInvalidParams {
		if err := c.relist(ctx, conn); err != nil {
			c.logger.Debug("mcp: re-listing tools after a tools/call error failed", "error", err)
		} else if !c.offers(name) {
			return fmt.Errorf("%w: %s", errToolGone, logText(name))
		}
	}
	return fmt.Errorf("mcp: %s %s: %w", methodToolsCall, logText(name), cause)
}

// isHeaderMismatch reports a -32020 HeaderMismatch answer (normally an
// HTTP 400 carrying it).
func isHeaderMismatch(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == codeHeaderMismatch
}

// callInfoFor frames a tools/call: modern adds Mcp-Name, and on HTTP the
// Mcp-Param-* values of the tool's validated x-mcp-header parameters.
func (c *serverClient) callInfoFor(conn *clientConn, name string, args map[string]any) callInfo {
	info := conn.frame(true)
	if conn.currentEra() != eraModern {
		return info
	}
	info.name = name
	if c.cfg.transport == transportHTTP {
		c.mu.Lock()
		var params []headerParam
		if c.tl != nil {
			params = c.tl.headers[name]
		}
		c.mu.Unlock()
		info.paramHeaders = headerValues(params, args)
	}
	return info
}
