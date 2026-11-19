package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// httpTStaggeredExpiry starts a legacy stateful e2eTHTTP whose answers
// to requests on an expired session are staggered: the n-th such
// request waits n×50ms before its 404. Sibling requests are then still
// in flight when the first 404 makes the client reconnect. held counts
// the requests that met the stagger.
func httpTStaggeredExpiry(t *testing.T) (srv *e2eTHTTP, held *atomic.Int64) {
	t.Helper()
	srv = &e2eTHTTP{
		style:    e2eTStyleStateful,
		sessions: map[string]bool{},
		seen:     map[string]int{},
		waiting:  map[string]chan *wireMessage{},
	}
	held = &atomic.Int64{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.Header.Get(httpHeaderSessionID); sid != "" && r.Method == http.MethodPost {
			srv.mu.Lock()
			live := srv.sessions[sid]
			srv.mu.Unlock()
			if !live {
				// Server-side latency for the rejection, not a test wait.
				select {
				case <-time.After(time.Duration(held.Add(1)) * 50 * time.Millisecond):
				case <-r.Context().Done():
				}
			}
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, held
}

// TestE2E_HTTPSessionExpiryLetsSiblingsFinish: when a session expires
// while several calls use it, the first rejection makes the client
// reconnect. The old transport is retired, not closed: the sibling
// calls get their own rejections (the server never processed them) and
// are retried on the new session, instead of being aborted and reported
// as an unknown outcome.
func TestE2E_HTTPSessionExpiryLetsSiblingsFinish(t *testing.T) {
	t.Parallel()
	srv, _ := httpTStaggeredExpiry(t)
	c := e2eTHTTPClient(t, srv, protocolLegacy, nil)
	e2eTStart(t, c)
	e2eTReady(t, c)
	e2eTEcho(t, c, "warm")

	srv.expireSessions()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	texts := make([]string, 3)
	for i := range errs {
		wg.Go(func() {
			res, err := c.callTool(t.Context(), "echo", map[string]any{"text": fmt.Sprint("call ", i)})
			texts[i], errs[i] = res.Text, err
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d = %v (connection lost: %v), want it retried on the new session",
				i, err, errors.Is(err, errConnectionLost))
		} else if want := fmt.Sprint("call ", i); texts[i] != want {
			t.Errorf("call %d = %q, want %q", i, texts[i], want)
		}
	}
	if got := srv.count(methodInitialize); got != 2 {
		t.Errorf("initialize sent %d times, want 2 (one reconnect)", got)
	}
	srv.checkClean(t)
}

// TestE2E_HTTPProbeExpiryLetsACallFinish: a health check that meets the
// expiry triggers the reconnect while a call is in flight on the same
// session; the call is retried on the new session, not aborted.
func TestE2E_HTTPProbeExpiryLetsACallFinish(t *testing.T) {
	t.Parallel()
	srv, held := httpTStaggeredExpiry(t)
	c := e2eTHTTPClient(t, srv, protocolLegacy, nil)
	e2eTStart(t, c)
	e2eTReady(t, c)
	e2eTEcho(t, c, "warm")

	srv.expireSessions()
	probeErr := make(chan error, 1)
	go func() { probeErr <- c.probe(t.Context()) }()
	e2eTEventually(t, "the server holding the probe", func() bool { return held.Load() >= 1 })
	res, err := c.callTool(t.Context(), "echo", map[string]any{"text": "during the reconnect"})
	if err != nil || res.Text != "during the reconnect" {
		t.Errorf("call = %+v, %v; want it retried on the new session", res, err)
	}
	if err := <-probeErr; !errors.Is(err, errSessionExpired) {
		t.Errorf("probe = %v, want errSessionExpired", err)
	}
	srv.checkClean(t)
}

// TestHTTP_RetireLetsInflightFinish: retire refuses new requests but
// lets an exchange already in flight get its real answer, and still
// sends notifications (an in-flight call may need to cancel or answer a
// server request). It returns once nothing is in flight, and does not
// close the transport.
func TestHTTP_RetireLetsInflightFinish(t *testing.T) {
	release := make(chan struct{})
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		if m.Method != methodToolsCall {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		select {
		case <-release:
			httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{"content":[]}`))
		case <-r.Context().Done():
		}
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	errc := make(chan error, 1)
	go func() {
		_, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{})
		errc <- err
	}()
	httpTEventually(t, func() bool { return len(srv.requests(methodToolsCall)) == 1 })

	retired := make(chan struct{})
	go func() {
		tr.retire(t.Context())
		close(retired)
	}()
	httpTEventually(t, func() bool { return tr.closedErr(methodPing) != nil })
	if _, err := tr.Call(t.Context(), &Request{Method: methodPing}, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Errorf("Call while retiring = %v, want errUnreachable", err)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); err != nil {
		t.Errorf("Notify while retiring = %v, want it sent", err)
	}
	select {
	case <-retired:
		t.Fatal("retire returned while a call was in flight")
	default:
	}

	close(release)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("in-flight call = %v, want its real answer", err)
		}
	case <-time.After(httpTWait):
		t.Fatal("the in-flight call did not finish")
	}
	select {
	case <-retired:
	case <-time.After(httpTWait):
		t.Fatal("retire did not return once nothing was in flight")
	}
	select {
	case <-tr.Done():
		t.Error("retire closed the transport")
	default:
	}
	if n := len(srv.requests(methodPing)); n != 0 {
		t.Errorf("server saw %d requests refused while retiring", n)
	}
}

// TestHTTP_RetireIsBoundedByCtx: retire stops waiting when its ctx ends;
// Close then aborts what is still in flight.
func TestHTTP_RetireIsBoundedByCtx(t *testing.T) {
	srv := httpTStart(t, func(_ http.ResponseWriter, r *http.Request, _ *wireMessage) { httpTBlock(r) })
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	errc := make(chan error, 1)
	go func() {
		_, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{})
		errc <- err
	}()
	httpTEventually(t, func() bool { return len(srv.requests(methodToolsCall)) == 1 })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tr.retire(ctx)
	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, errConnectionLost) {
			t.Errorf("in-flight call after Close = %v, want errConnectionLost", err)
		}
	case <-time.After(httpTWait):
		t.Fatal("Close did not abort the in-flight call")
	}
}

// TestHTTP_RetiredExpiredSessionReportsExpiry: a request that reaches a
// transport whose session expired after it was retired or closed was
// never sent, so it reports errSessionExpired (the client retries it on
// the new session), not errUnreachable.
func TestHTTP_RetiredExpiredSessionReportsExpiry(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, _ *wireMessage) {
		w.WriteHeader(http.StatusAccepted)
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	tr.mu.Lock()
	tr.expired = true
	tr.mu.Unlock()
	tr.retire(t.Context())
	if _, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{}); !errors.Is(err, errSessionExpired) {
		t.Errorf("Call on a retired, expired transport = %v, want errSessionExpired", err)
	}
	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{}); !errors.Is(err, errSessionExpired) {
		t.Errorf("Call on a closed, expired transport = %v, want errSessionExpired", err)
	}
	if n := len(srv.requests("")); n != 0 {
		t.Errorf("server saw %d requests", n)
	}
}
