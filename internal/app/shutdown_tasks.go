package app

import (
	"context"
	"log/slog"
	"time"
)

// shutdownTasksTimeout bounds how long Serve waits for the shutdown
// tail before closing resources anyway — a backstop against a wedged
// tail step, degrading loudly instead of racily.
const shutdownTasksTimeout = 60 * time.Second

// shutdownTasks coordinates the post-drain shutdown tail (archive
// conversations, MQTT offline, shutdown checkpoint) with Serve's
// return. The contract it exists to enforce: any tail work that runs,
// runs to completion before Serve returns and its deferred shutdown()
// closes the stores the tail reads — and on a fatal server error,
// where ctx was never cancelled, the tail must not run at all, not
// even later when the command caller's deferred cancel finally fires.
type shutdownTasks struct {
	logger  *slog.Logger
	timeout time.Duration
	done    chan struct{}
	abort   chan struct{}
}

func newShutdownTasks(logger *slog.Logger, timeout time.Duration) *shutdownTasks {
	return &shutdownTasks{
		logger:  logger,
		timeout: timeout,
		done:    make(chan struct{}),
		abort:   make(chan struct{}),
	}
}

// watch arms the tail: it runs once ctx is cancelled. An abort releases
// the watcher without running it. If cancel and abort race, the select
// may pick either — safe both ways, because finish always waits for
// done, so a tail that does start completes before resources close.
func (s *shutdownTasks) watch(ctx context.Context, tail func()) {
	go func() {
		defer close(s.done)
		select {
		case <-ctx.Done():
		case <-s.abort:
			return
		}
		tail()
	}()
}

// finish synchronizes Serve's return with the watcher. fatal means
// Serve is returning without a cancelled ctx (server error): the
// watcher is released via abort so the tail never fires against the
// stores the caller is about to close. Either way finish waits for the
// watcher to acknowledge, bounded by the timeout.
func (s *shutdownTasks) finish(fatal bool) {
	if fatal {
		close(s.abort)
	}
	select {
	case <-s.done:
	case <-time.After(s.timeout):
		s.logger.Warn("shutdown tasks still running at exit; closing resources anyway")
	}
}
