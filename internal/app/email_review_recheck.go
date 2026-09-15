package app

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// A review loop's recheck is how work a wake left behind wakes the loop
// again: a batch larger than one queue_pull, an item the loop deferred,
// or a wake the bus did not deliver. Every wake arms the loop's recheck,
// and review_delay later the recheck wakes the loop again if work that
// wake announced still waits unchanged (leftoverDue). It runs on its own
// timer, whether or not mail is polled, so leftover work never waits
// for a poll that may not come. An attempt that could not read the
// queue arms the recheck too, as its retry, for once the floor has
// passed.
//
// Each loop has at most one recheck pending: arming replaces it, and a
// read that finds the partition empty disarms it, so an idle queue holds
// no timer. A read that sends no wake while announced work waits arms
// it again for when the last wake's interval ends, so a retry that
// replaced it cannot strand that work (keepLeftoverRecheck). The
// recheck wakes through wakeFor under w.mu like every other path, so it
// and another wake never announce the same work twice.

// emailReviewRecheckFloor is the shortest time from a wake to its
// recheck, so a review_delay of seconds cannot turn a deferred item into
// a wake every few seconds.
const emailReviewRecheckFloor = time.Minute

// emailReviewRecheck is one loop's pending recheck. gen tells its timer
// callback whether a later arm has replaced it.
type emailReviewRecheck struct {
	timer *time.Timer
	gen   uint64
}

// recheckInterval is how long after a wake a loop's recheck runs:
// review_delay, and never less than the floor.
func (w *emailReviewWaker) recheckInterval(name string) time.Duration {
	return max(w.loops[name].delay, w.recheckFloor)
}

// armRecheck arms name's recheck to try a wake that due approves, after
// the given interval, replacing any recheck already pending. It does
// nothing once the waker has stopped.
func (w *emailReviewWaker) armRecheck(name, reason string, after time.Duration, due emailReviewDue) {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if w.stopped {
		return
	}
	if cur, ok := w.rechecks[name]; ok {
		cur.timer.Stop()
	}
	w.recheckGen++
	gen := w.recheckGen
	// The callback takes lifeMu, so it cannot look for this recheck
	// before it is stored below.
	w.rechecks[name] = &emailReviewRecheck{
		gen:   gen,
		timer: time.AfterFunc(after, func() { w.fireRecheck(name, reason, gen, due) }),
	}
}

// keepLeftoverRecheck arms name's recheck for announced work still
// waiting after an attempt that sent no wake, for when the last wake's
// recheck interval ends. A retry replaces the recheck and comes sooner,
// and when it finds nothing due yet, this is what keeps that work from
// waiting for a wake no timer will send. Caller holds w.mu.
func (w *emailReviewWaker) keepLeftoverRecheck(name string, split loopqueue.PendingSplit) {
	last, woke := w.lastWake[name]
	if !woke || split.Through == 0 {
		return
	}
	w.armRecheck(name, emailReviewReasonLeftover, max(0, w.recheckInterval(name)-w.now().Sub(last)), w.leftoverDue)
}

// disarmRecheck cancels name's pending recheck, if it has one.
func (w *emailReviewWaker) disarmRecheck(name string) {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if cur, ok := w.rechecks[name]; ok {
		cur.timer.Stop()
		delete(w.rechecks, name)
	}
}

// fireRecheck runs when a recheck's timer fires, and launches its wake
// attempt unless a later arm replaced it, a read disarmed it, or the
// waker stopped.
func (w *emailReviewWaker) fireRecheck(name, reason string, gen uint64, due emailReviewDue) {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	cur, ok := w.rechecks[name]
	if !ok || cur.gen != gen {
		return
	}
	delete(w.rechecks, name)
	w.launchLocked(name, reason, due)
}

// launch runs one wake attempt on a worker goroutine, for the callers
// that must not block: the debounce fire and the recheck timer. The
// attempt runs under the waker's life context, which stop cancels, and
// wakeFor bounds it by wakeTimeout, so no worker outlives either.
func (w *emailReviewWaker) launch(name, reason string, due emailReviewDue) {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	w.launchLocked(name, reason, due)
}

// launchLocked is launch with w.lifeMu held. Holding it orders every
// launch before stop's wait for the workers, so a worker is never added
// to a wait already under way.
func (w *emailReviewWaker) launchLocked(name, reason string, due emailReviewDue) {
	if w.stopped {
		return
	}
	w.workers.Add(1)
	go func() {
		defer w.workers.Done()
		w.wakeFor(w.life, name, reason, due)
	}()
}

// stop ends the waker at shutdown. No debounce fires and no recheck runs
// after it, an attempt in flight is cancelled, and stop returns once
// every worker has returned. Work still queued waits for the next boot
// sweep.
func (w *emailReviewWaker) stop() {
	if w == nil {
		return
	}
	w.lifeMu.Lock()
	if w.stopped {
		w.lifeMu.Unlock()
		return
	}
	w.stopped = true
	for name, cur := range w.rechecks {
		cur.timer.Stop()
		delete(w.rechecks, name)
	}
	w.lifeMu.Unlock()
	for _, name := range w.names() {
		w.queue.SetWakeOnEnqueue(name, 0, 0, nil)
	}
	w.stopLife()
	w.workers.Wait()
}

// eitherDue approves what either predicate approves.
func eitherDue(a, b emailReviewDue) emailReviewDue {
	return func(name string, split loopqueue.PendingSplit) bool {
		return a(name, split) || b(name, split)
	}
}
