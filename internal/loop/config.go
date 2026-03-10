package loop

import "time"

// State represents the lifecycle state of a running loop.
type State string

// Loop states.
const (
	// StatePending means the loop is registered but not yet started
	// (e.g., waiting for a StartWhen condition).
	StatePending State = "pending"
	// StateSleeping means the loop is between iterations, waiting for
	// the next wake.
	StateSleeping State = "sleeping"
	// StateProcessing means the loop is actively running an LLM
	// iteration.
	StateProcessing State = "processing"
	// StateError means the loop's last iteration failed. It will
	// retry on the next sleep cycle.
	StateError State = "error"
	// StateStopped means the loop has been cancelled and is no
	// longer running.
	StateStopped State = "stopped"
)

// RetriggerMode determines what happens when a loop's start condition
// fires again while the loop is already running.
type RetriggerMode int

const (
	// RetriggerSingle ignores re-triggers while the loop is running.
	RetriggerSingle RetriggerMode = iota
	// RetriggerRestart cancels the current loop and starts fresh.
	RetriggerRestart
	// RetriggerQueue queues the trigger and runs after current completes.
	RetriggerQueue
	// RetriggerSpawn spawns another instance of the loop.
	RetriggerSpawn
)

// Config holds the configuration for a loop. All fields with zero values
// use sensible defaults.
type Config struct {
	// Name is the unique identifier for this loop. Required.
	Name string

	// Task is the LLM prompt for each iteration. It describes what
	// the loop should observe, check, or do on each wake.
	Task string

	// Tags are capability tags for tool scoping. When non-empty,
	// the loop's tool registry is filtered to tools matching these
	// tags (plus always-active tags).
	Tags []string

	// ExcludeTools lists tool names to exclude from the loop's
	// available tools.
	ExcludeTools []string

	// SleepMin is the minimum sleep duration between iterations.
	// Default: 30s.
	SleepMin time.Duration

	// SleepMax is the maximum sleep duration between iterations.
	// Default: 5m.
	SleepMax time.Duration

	// SleepDefault is the initial sleep duration before the loop
	// self-adjusts. Default: 1m.
	SleepDefault time.Duration

	// Jitter is the randomization factor applied to sleep durations
	// to break periodicity. Range [0.0, 1.0]. Default: 0.2.
	Jitter float64

	// MaxDuration is the maximum wall-clock time the loop may run.
	// Zero means unlimited.
	MaxDuration time.Duration

	// MaxIter is the maximum number of iterations the loop may
	// execute. Zero means unlimited.
	MaxIter int

	// Supervisor enables frontier model dice rolls. When true, a
	// fraction of iterations (controlled by SupervisorProb) use a
	// more capable model for oversight.
	Supervisor bool

	// SupervisorProb is the probability [0.0, 1.0] that a given
	// iteration uses the supervisor model. Only meaningful when
	// Supervisor is true. Default: 0.1.
	SupervisorProb float64

	// QualityFloor is the minimum model quality rating for normal
	// iterations. Zero uses the router default.
	QualityFloor int

	// SupervisorQualityFloor is the minimum model quality rating
	// for supervisor iterations. Zero uses the router default.
	SupervisorQualityFloor int

	// OnRetrigger determines behavior when the loop's start
	// condition fires again while running. Default: RetriggerSingle.
	OnRetrigger RetriggerMode

	// Metadata holds arbitrary key/value pairs for the loop.
	Metadata map[string]string

	// ParentID is the loop ID of the parent that spawned this loop,
	// if any. Empty for top-level loops.
	ParentID string
}

const (
	defaultSleepMin     = 30 * time.Second
	defaultSleepMax     = 5 * time.Minute
	defaultSleepDefault = 1 * time.Minute
	defaultJitter       = 0.2
	defaultSuperProb    = 0.1
)

// applyDefaults fills in zero-valued fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.SleepMin == 0 {
		c.SleepMin = defaultSleepMin
	}
	if c.SleepMax == 0 {
		c.SleepMax = defaultSleepMax
	}
	if c.SleepDefault == 0 {
		c.SleepDefault = defaultSleepDefault
	}
	if c.Jitter == 0 {
		c.Jitter = defaultJitter
	}
	if c.Supervisor && c.SupervisorProb == 0 {
		c.SupervisorProb = defaultSuperProb
	}
}

// Status is a snapshot of a loop's current state and metrics,
// suitable for external inspection via the registry.
type Status struct {
	// ID is the unique loop identifier.
	ID string `json:"id"`
	// Name is the human-readable loop name.
	Name string `json:"name"`
	// State is the current lifecycle state.
	State State `json:"state"`
	// ParentID is the ID of the parent loop, if any.
	ParentID string `json:"parent_id,omitempty"`
	// StartedAt is when the loop was started.
	StartedAt time.Time `json:"started_at"`
	// LastWakeAt is when the loop last began an iteration.
	LastWakeAt time.Time `json:"last_wake_at,omitempty"`
	// Iterations is the total number of completed iterations.
	Iterations int `json:"iterations"`
	// TotalInputTokens is the cumulative input tokens across all iterations.
	TotalInputTokens int `json:"total_input_tokens"`
	// TotalOutputTokens is the cumulative output tokens across all iterations.
	TotalOutputTokens int `json:"total_output_tokens"`
	// LastError is the error message from the most recent failed iteration.
	LastError string `json:"last_error,omitempty"`
	// Config is a copy of the loop's configuration.
	Config Config `json:"config"`
}
