package loopqueue

import (
	"time"
)

// DefaultWakeDebounce is the trailing-edge window for event-driven
// consumers: long enough to coalesce an event burst (an HA automation
// storm, a chatty MQTT topic) into one consumer wake, short enough
// that a lone event still lands promptly.
const DefaultWakeDebounce = 2 * time.Second

// DefaultWakeMaxWait is the anti-starvation bound on the debounce:
// continuous chatter keeps resetting a trailing-edge window, so the
// wake fires no later than this long after the first un-drained
// enqueue regardless.
const DefaultWakeMaxWait = 15 * time.Second

// wakeRegistration is one consumer's debounced wake state. All fields
// are guarded by the parent Store's wakeMu.
type wakeRegistration struct {
	debounce time.Duration
	maxWait  time.Duration
	fire     func()

	timer      *time.Timer // pending timer; nil when idle
	firstArmed time.Time   // when the current un-fired burst began
	// gen invalidates stale timer callbacks: every re-arm and every
	// fire bumps it, and a callback only proceeds when its captured
	// generation is still current. Reset on a fired-or-firing
	// time.AfterFunc timer can let an expired callback run alongside
	// the re-armed one; the generation check turns that race into a
	// no-op instead of a duplicate wake.
	gen uint64
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
// on a timer goroutine and must not block — a consumer whose drain
// does real work (bus sends, I/O) should hand off to its own worker
// and serialize its drains, since a fire can coincide with an
// externally-triggered drain (e.g. a boot sweep). At-most-one fire
// per burst is guaranteed by a per-registration generation guard.
// Registrations are expected at wiring time (before traffic) and a
// second call for the same consumer replaces the first. Passing a nil
// fire removes the registration. The debounce state is process-local —
// a pending burst does not survive a restart, which is why wiring
// pairs registration with a boot-time sweep of non-empty partitions.
func (s *Store) SetWakeOnEnqueue(consumerLoop string, debounce, maxWait time.Duration, fire func()) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wakes == nil {
		s.wakes = make(map[string]*wakeRegistration)
	}
	if existing, ok := s.wakes[consumerLoop]; ok {
		existing.gen++ // invalidate any in-flight callback
		if existing.timer != nil {
			existing.timer.Stop()
		}
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
// enqueue. No-op for consumers without a registration. Each arm
// replaces the pending timer with a fresh one under a new generation
// — never Reset, which on an already-fired [time.AfterFunc] timer can
// let a stale callback run alongside the re-armed one.
func (s *Store) armWake(consumerLoop string) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	reg, ok := s.wakes[consumerLoop]
	if !ok {
		return
	}

	now := time.Now()
	delay := reg.debounce
	if reg.timer == nil {
		// First enqueue of a burst: stamp the burst origin for the
		// maxWait bound.
		reg.firstArmed = now
	} else {
		reg.timer.Stop()
		// Subsequent enqueue: extend the trailing window, but never
		// past firstArmed+maxWait — a continuously chatty producer
		// must not starve the drain.
		if remaining := reg.firstArmed.Add(reg.maxWait).Sub(now); remaining < delay {
			delay = remaining
		}
		if delay < 0 {
			delay = 0
		}
	}
	reg.gen++
	gen := reg.gen
	reg.timer = time.AfterFunc(delay, func() { s.fireWake(consumerLoop, gen) })
}

// fireWake runs a registration's callback — unless the captured
// generation is stale (the timer was re-armed or the registration
// replaced while this callback was in flight) — and returns the
// registration to idle so the next enqueue starts a fresh burst.
func (s *Store) fireWake(consumerLoop string, gen uint64) {
	s.wakeMu.Lock()
	reg, ok := s.wakes[consumerLoop]
	if !ok || reg.gen != gen {
		s.wakeMu.Unlock()
		return
	}
	reg.gen++ // consume this generation; any other in-flight callback is stale
	reg.timer = nil
	reg.firstArmed = time.Time{}
	fire := reg.fire
	s.wakeMu.Unlock()

	fire()
}
