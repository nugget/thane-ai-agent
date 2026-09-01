package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStreamIdleTimeout marks a model response that stopped delivering bytes
// long enough for the endpoint's stream-idle guard to abandon it.
var ErrStreamIdleTimeout = errors.New("model stream idle timeout")

// StreamIdleTimeoutError reports the silence window that expired. It also
// classifies as [context.DeadlineExceeded], so the agent's ordinary timeout
// retry and recovery path handles a stalled provider response.
type StreamIdleTimeoutError struct {
	Idle time.Duration
}

// Error implements error.
func (e *StreamIdleTimeoutError) Error() string {
	return fmt.Sprintf("model stream idle timeout after %s with no bytes", e.Idle)
}

// Is supports both provider-specific and generic timeout classification.
func (e *StreamIdleTimeoutError) Is(target error) bool {
	return target == ErrStreamIdleTimeout || target == context.DeadlineExceeded
}

// DefaultStreamIdleTimeout bounds how long a model server may deliver
// nothing at all before the request is abandoned.
//
// It is generous on purpose. The silence being measured is not only the
// gap between tokens — a server is quiet through prefill too, and a
// large prompt against a loaded local runner can take tens of seconds
// before the first byte moves. Production's slowest observed whole
// generation is under four minutes, so two minutes of complete silence
// sits well outside normal behavior while still bounding a hang that
// would otherwise last until the process restarts.
const DefaultStreamIdleTimeout = 2 * time.Minute

// newStreamIdleGuard wraps a response body so the request is cancelled
// when the server stops sending for idle. cancel must belong to the
// context the request was built with — cancelling anything else leaves
// the blocked Read exactly where it was.
//
// This closes the gap ResponseHeaderTimeout leaves behind. That timeout
// stops applying the moment headers arrive, and these clients carry no
// total timeout by design, because a long generation is not a hung
// request. Between the first header and the last byte there was
// therefore nothing bounding a server that accepted a request and then
// went quiet: the read blocks forever, the iteration never finishes, and
// the loop waiting on it never wakes again.
//
// The guard measures silence rather than duration, so a slow answer is
// still allowed to be slow. Cancelling the request is what unblocks the
// Read; the transport aborts and surfaces a context error, which the
// caller reports as a failed turn rather than a successful empty one.
func newStreamIdleGuard(body io.ReadCloser, cancel context.CancelFunc, idle time.Duration) io.ReadCloser {
	g := &streamIdleGuard{
		ReadCloser: body,
		cancel:     cancel,
		idle:       idle,
		done:       make(chan struct{}),
	}
	g.touch()
	if idle > 0 {
		go g.watch()
	}
	return g
}

// streamIdleGuard cancels its request when reads stop producing bytes.
// Activity is recorded as a timestamp and checked by a poller rather
// than driven through a timer, so the reader and the watchdog never
// contend over the same timer state.
type streamIdleGuard struct {
	io.ReadCloser
	cancel   context.CancelFunc
	idle     time.Duration
	last     atomic.Int64
	timedOut atomic.Bool

	once sync.Once
	done chan struct{}
}

func (g *streamIdleGuard) touch() { g.last.Store(time.Now().UnixNano()) }

// watch cancels the request once the idle window passes with no bytes
// read. It polls at a fraction of the window because precision is not
// the point — the difference between abandoning a dead stream at two
// minutes and at two and a half is nothing next to abandoning it never.
func (g *streamIdleGuard) watch() {
	interval := g.idle / 4
	if interval <= 0 {
		interval = g.idle
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-g.done:
			return
		case now := <-tick.C:
			if now.UnixNano()-g.last.Load() >= int64(g.idle) {
				g.timedOut.Store(true)
				g.cancel()
				return
			}
		}
	}
}

func (g *streamIdleGuard) Read(p []byte) (int, error) {
	n, err := g.ReadCloser.Read(p)
	if n > 0 {
		g.touch()
	}
	if err != nil && g.timedOut.Load() {
		return n, &StreamIdleTimeoutError{Idle: g.idle}
	}
	return n, err
}

// Close stops the watchdog and releases the request context. Safe to
// call repeatedly: both the caller's defer and the http client may close
// a response body.
func (g *streamIdleGuard) Close() error {
	g.once.Do(func() { close(g.done) })
	// Cancel rather than leak. The request is over either way, and a
	// cancel func outliving its request is how a transport ends up
	// holding a connection nothing will ever read again.
	g.cancel()
	return g.ReadCloser.Close()
}
