package loopqueue

import (
	"time"
)

// Default debounce shape for event-driven consumers. Chosen for wake
// semantics: long enough to coalesce an event burst (an HA automation
// storm, a chatty MQTT topic) into one consumer wake, short enough
// that a lone event still lands promptly. MaxWait is the
// anti-starvation bound — continuous chatter keeps resetting a
// trailing-edge debounce, so the wake fires no later than MaxWait
// after the first un-drained enqueue regardless.
const (
	DefaultWakeDebounce = 2 * time.Second
	DefaultWakeMaxWait  = 15 * time.Second
)

// wakeRegistration is one consumer's debounced wake state. All fields
// are guarded by the parent Store's wakeMu.
type wakeRegistration struct {
	debounce time.Duration
	maxWait  time.Duration
	fire     func()

	timer      *time.Timer // pending trailing-edge timer; nil when idle
	firstArmed time.Time   // when the current un-fired burst began
}

// SetWakeOnEnqueue registers a debounced wake for consumerLoop: after
// any successful [Store.Enqueue] into that partition, fire is invoked
// once per burst — trailing-edge debounced, so a storm of enqueues
// coalesces into a single wake after the burst quiets, with maxWait
// bounding how long continuous chatter can postpone it. This is the
// WakeOnEnqueue seam from #1024/#1025: self-paced consumers (the
// archivist) simply never register and keep draining on their own
// cadence; event-driven consumers (MQTT wake dispatch, #1033) register
// so trigger latency stays low without trigger rate driving work rate.
//
// debounce/maxWait <= 0 fall back to the package defaults. fire runs
// on a timer goroutine and must not block; registrations are expected
// at wiring time (before traffic) and a second call for the same
// consumer replaces the first. Passing a nil fire removes the
// registration. The debounce state is process-local — a pending burst
// does not survive a restart, which is why wiring pairs registration
// with a boot-time sweep of non-empty partitions.
func (s *Store) SetWakeOnEnqueue(consumerLoop string, debounce, maxWait time.Duration, fire func()) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wakes == nil {
		s.wakes = make(map[string]*wakeRegistration)
	}
	if existing, ok := s.wakes[consumerLoop]; ok && existing.timer != nil {
		existing.timer.Stop()
	}
	if fire == nil {
		delete(s.wakes, consumerLoop)
		return
	}
	if debounce <= 0 {
		debounce = DefaultWakeDebounce
	}
	if maxWait <= 0 {
		maxWait = DefaultWakeMaxWait
	}
	s.wakes[consumerLoop] = &wakeRegistration{
		debounce: debounce,
		maxWait:  maxWait,
		fire:     fire,
	}
}

// armWake starts or extends consumerLoop's debounce window after an
// enqueue. No-op for consumers without a registration.
func (s *Store) armWake(consumerLoop string) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	reg, ok := s.wakes[consumerLoop]
	if !ok {
		return
	}

	now := time.Now()
	if reg.timer == nil {
		// First enqueue of a burst: start the trailing window and
		// stamp the burst origin for the maxWait bound.
		reg.firstArmed = now
		reg.timer = time.AfterFunc(reg.debounce, func() { s.fireWake(consumerLoop) })
		return
	}

	// Subsequent enqueue: extend the trailing window, but never past
	// firstArmed+maxWait — a continuously chatty producer must not
	// starve the drain.
	remainingToCap := reg.firstArmed.Add(reg.maxWait).Sub(now)
	delay := reg.debounce
	if remainingToCap < delay {
		delay = remainingToCap
	}
	if delay < 0 {
		delay = 0
	}
	reg.timer.Reset(delay)
}

// fireWake runs a registration's callback and returns it to idle so
// the next enqueue starts a fresh burst.
func (s *Store) fireWake(consumerLoop string) {
	s.wakeMu.Lock()
	reg, ok := s.wakes[consumerLoop]
	if !ok {
		s.wakeMu.Unlock()
		return
	}
	reg.timer = nil
	reg.firstArmed = time.Time{}
	fire := reg.fire
	s.wakeMu.Unlock()

	fire()
}
