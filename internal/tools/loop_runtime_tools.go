package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

const (
	defaultLoopStatusLimit = 25
	maxLoopStatusLimit     = 200
)

type loopStatusView struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	State              looppkg.State      `json:"state"`
	Operation          looppkg.Operation  `json:"operation,omitempty"`
	Completion         looppkg.Completion `json:"completion,omitempty"`
	ParentID           string             `json:"parent_id,omitempty"`
	StartedAt          time.Time          `json:"started_at"`
	LastWakeAt         time.Time          `json:"last_wake_at,omitempty"`
	Iterations         int                `json:"iterations"`
	Attempts           int                `json:"attempts"`
	TotalInputTokens   int                `json:"total_input_tokens"`
	TotalOutputTokens  int                `json:"total_output_tokens"`
	LastInputTokens    int                `json:"last_input_tokens,omitempty"`
	LastOutputTokens   int                `json:"last_output_tokens,omitempty"`
	LastError          string             `json:"last_error,omitempty"`
	ConsecutiveErrors  int                `json:"consecutive_errors,omitempty"`
	HandlerOnly        bool               `json:"handler_only,omitempty"`
	EventDriven        bool               `json:"event_driven,omitempty"`
	LastSupervisorIter int                `json:"last_supervisor_iter,omitempty"`
	ActiveTags         []string           `json:"active_tags,omitempty"`
	Metadata           map[string]string  `json:"metadata,omitempty"`
}

// LoopRuntimeToolDeps wires the live loop registry and ad hoc launch path
// into the tool registry so the model can inspect and control currently
// running loops.
type LoopRuntimeToolDeps struct {
	Registry   *looppkg.Registry
	LaunchLoop func(context.Context, looppkg.Launch) (looppkg.LaunchResult, error)
}

// ConfigureLoopRuntimeTools stores the runtime dependencies needed by the
// live loop tool family and registers the tools.
func (r *Registry) ConfigureLoopRuntimeTools(deps LoopRuntimeToolDeps) {
	r.liveLoopRegistry = deps.Registry
	r.launchLoop = deps.LaunchLoop
	r.registerLoopRuntimeTools()
}

func (r *Registry) registerLoopRuntimeTools() {
	if r.liveLoopRegistry == nil {
		return
	}

	r.Register(&Tool{
		Name:        "loop_status",
		Description: "Inspect the live loop registry. Returns compact structured status for currently running loops, with optional filters by query text, state, operation, and a result limit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring match against loop ID, name, parent ID, and metadata values.",
				},
				"state": map[string]any{
					"type":        "string",
					"description": "Optional exact lifecycle state filter such as pending, sleeping, waiting, processing, error, or stopped.",
				},
				"operation": map[string]any{
					"type":        "string",
					"enum":        []string{"request_reply", "background_task", "service"},
					"description": "Optional exact operation filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum loops to return (default %d, max %d).", defaultLoopStatusLimit, maxLoopStatusLimit),
				},
			},
		},
		Handler: r.handleLoopStatus,
	})

	r.Register(&Tool{
		Name:            "set_next_sleep",
		AlwaysAvailable: true,
		Description:     "Request the next sleep duration for the current running timer-driven service loop. This is the native loops-ng sleep-control tool used by persistent background services such as metacog-style loops. The requested duration is clamped to the loop's configured sleep_min and sleep_max bounds.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration": map[string]any{
					"description": "Requested next sleep duration. Prefer a Go duration string like \"15m\" or \"1h\". Numeric values are also accepted and interpreted as minutes for tolerant local-model compatibility.",
					"anyOf": []map[string]any{
						{"type": "string"},
						{"type": "number"},
						{"type": "integer"},
					},
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short explanation of why this duration was chosen. Logged for operator visibility.",
				},
			},
			"required": []string{"duration"},
		},
		Handler: r.handleSetNextSleep,
	})

	r.Register(&Tool{
		Name:        "spawn_loop",
		Description: "Launch an ad hoc loop immediately using the loops-ng launch contract without persisting a loop definition. Use this for temporary services, detached background research that should report back later, or one-shot request/reply runs that do not belong in the durable loop-definition registry. For async work, prefer operation=background_task. If completion is omitted there, the runtime infers the most natural detached delivery target from the current origin context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"launch": map[string]any{
					"type":        "object",
					"description": "Loops-ng launch object. Include spec, task, and any per-launch routing or completion overrides. The most common detached shape is a task plus spec.operation=background_task; completion may be omitted when the current conversation or channel context should decide the callback target.",
				},
			},
			"required": []string{"launch"},
		},
		Handler: r.handleSpawnLoop,
	})

	r.Register(&Tool{
		Name:        "stop_loop",
		Description: "Stop one currently running loop by loop_id or exact name. Prefer loop_id when available. If name is ambiguous, the tool returns the candidate IDs so you can retry precisely.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"loop_id": map[string]any{
					"type":        "string",
					"description": "Exact live loop ID to stop. Preferred when you already have it from loop_status.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Exact live loop name to stop when loop_id is not known.",
				},
			},
		},
		Handler: r.handleStopLoop,
	})
}

func (r *Registry) handleLoopStatus(_ context.Context, args map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	query := strings.ToLower(ldStringArg(args, "query"))
	state := strings.ToLower(ldStringArg(args, "state"))
	operation := looppkg.Operation(ldStringArg(args, "operation"))
	limit := clampLoopListLimit(ldIntArg(args, "limit"), defaultLoopStatusLimit, maxLoopStatusLimit)

	statuses := r.liveLoopRegistry.Statuses()
	filtered := make([]loopStatusView, 0, len(statuses))
	for _, status := range statuses {
		if !matchLoopStatus(status, query, state, operation) {
			continue
		}
		filtered = append(filtered, summarizeLoopStatus(status))
		if len(filtered) >= limit {
			break
		}
	}

	maxLoops := r.liveLoopRegistry.MaxLoops()
	remaining := 0
	if maxLoops > 0 {
		remaining = maxLoops - r.liveLoopRegistry.ActiveCount()
		if remaining < 0 {
			remaining = 0
		}
	}

	return ldMarshalToolJSON(map[string]any{
		"status":             "ok",
		"active_count":       r.liveLoopRegistry.ActiveCount(),
		"max_loops":          maxLoops,
		"remaining_capacity": remaining,
		"filters": map[string]any{
			"query":     strings.TrimSpace(query),
			"state":     state,
			"operation": operation,
			"limit":     limit,
		},
		"loops": filtered,
	})
}

func (r *Registry) handleSetNextSleep(ctx context.Context, args map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	loopID := strings.TrimSpace(LoopIDFromContext(ctx))
	if loopID == "" {
		return "", fmt.Errorf("set_next_sleep can only be called from a running timer-driven service loop")
	}
	live := r.liveLoopRegistry.Get(loopID)
	if live == nil {
		return "", fmt.Errorf("current loop %q not found", loopID)
	}

	status := live.Status()
	if status.Config.Operation != looppkg.OperationService {
		return "", fmt.Errorf("set_next_sleep is only available to service loops; current loop %q uses %q", status.Name, status.Config.Operation)
	}
	if status.EventDriven {
		return "", fmt.Errorf("set_next_sleep is unavailable for event-driven service loops; current loop %q waits for events instead of sleeping on a timer", status.Name)
	}

	requested, requestedText, err := parseNextSleepDurationArg(args)
	if err != nil {
		return "", err
	}
	applied := requested
	if applied < status.Config.SleepMin {
		applied = status.Config.SleepMin
	}
	if applied > status.Config.SleepMax {
		applied = status.Config.SleepMax
	}
	reason := ldStringArg(args, "reason")
	clamped := applied != requested
	live.SetNextSleep(applied)

	logging.Logger(ctx).Info(
		"loop next sleep set",
		"loop_id", status.ID,
		"loop_name", status.Name,
		"requested", requested.Round(time.Second),
		"applied", applied.Round(time.Second),
		"sleep_min", status.Config.SleepMin,
		"sleep_max", status.Config.SleepMax,
		"reason", reason,
		"clamped", clamped,
	)

	return ldMarshalToolJSON(map[string]any{
		"status":        "ok",
		"loop_id":       status.ID,
		"loop_name":     status.Name,
		"requested":     requestedText,
		"applied":       applied.String(),
		"clamped":       clamped,
		"sleep_min":     status.Config.SleepMin.String(),
		"sleep_max":     status.Config.SleepMax.String(),
		"sleep_default": status.Config.SleepDefault.String(),
		"reason":        reason,
	})
}

func (r *Registry) handleSpawnLoop(ctx context.Context, args map[string]any) (string, error) {
	if r.launchLoop == nil {
		return "", fmt.Errorf("loop launch is not configured")
	}
	if _, ok := args["launch"]; !ok {
		return "", fmt.Errorf("launch is required")
	}
	launch, err := decodeLoopLaunchArg(args, "launch")
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}
	launch, completion := applyAdHocLoopLaunchContextDefaults(ctx, launch)
	result, err := r.launchLoop(ctx, launch)
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"result":     result,
		"completion": completion,
	})
}

func parseNextSleepDurationArg(args map[string]any) (time.Duration, string, error) {
	raw, ok := args["duration"]
	if !ok {
		return 0, "", fmt.Errorf("duration is required")
	}

	var durStr string
	switch v := raw.(type) {
	case string:
		durStr = strings.TrimSpace(v)
	case int:
		durStr = fmt.Sprintf("%dm", v)
	case int64:
		durStr = fmt.Sprintf("%dm", v)
	case float32:
		durStr = fmt.Sprintf("%gm", v)
	case float64:
		durStr = fmt.Sprintf("%gm", v)
	default:
		return 0, "", fmt.Errorf("duration must be a Go duration string or a numeric minute count")
	}
	if durStr == "" {
		return 0, "", fmt.Errorf("duration is required")
	}
	d, err := time.ParseDuration(durStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid duration %q: %w", durStr, err)
	}
	return d, durStr, nil
}

func (r *Registry) handleStopLoop(_ context.Context, args map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	loopID := ldStringArg(args, "loop_id")
	name := ldStringArg(args, "name")
	if loopID == "" && name == "" {
		return "", fmt.Errorf("loop_id or name is required")
	}

	var (
		status looppkg.Status
		err    error
	)
	if loopID != "" {
		live := r.liveLoopRegistry.Get(loopID)
		if live == nil {
			return "", fmt.Errorf("loop %q not found", loopID)
		}
		status = live.Status()
		err = r.liveLoopRegistry.StopLoop(loopID)
	} else {
		matches := r.liveLoopRegistry.FindByName(name)
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("loop named %q not found", name)
		case 1:
			status = matches[0].Status()
			err = r.liveLoopRegistry.StopLoopByName(name)
		default:
			ids := make([]string, 0, len(matches))
			for _, live := range matches {
				ids = append(ids, live.ID())
			}
			return "", fmt.Errorf("loop name %q is ambiguous; retry with loop_id from %v", name, ids)
		}
	}
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status": "ok",
		"loop":   status,
	})
}

func clampLoopListLimit(raw, def, max int) int {
	switch {
	case raw <= 0:
		return def
	case raw > max:
		return max
	default:
		return raw
	}
}

func matchLoopStatus(status looppkg.Status, query, state string, operation looppkg.Operation) bool {
	if state != "" && strings.ToLower(string(status.State)) != state {
		return false
	}
	if operation != "" && status.Config.Operation != operation {
		return false
	}
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(status.ID), query) || strings.Contains(strings.ToLower(status.Name), query) || strings.Contains(strings.ToLower(status.ParentID), query) {
		return true
	}
	for _, value := range status.Config.Metadata {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func summarizeLoopStatus(status looppkg.Status) loopStatusView {
	return loopStatusView{
		ID:                 status.ID,
		Name:               status.Name,
		State:              status.State,
		Operation:          status.Config.Operation,
		Completion:         status.Config.Completion,
		ParentID:           status.ParentID,
		StartedAt:          status.StartedAt,
		LastWakeAt:         status.LastWakeAt,
		Iterations:         status.Iterations,
		Attempts:           status.Attempts,
		TotalInputTokens:   status.TotalInputTokens,
		TotalOutputTokens:  status.TotalOutputTokens,
		LastInputTokens:    status.LastInputTokens,
		LastOutputTokens:   status.LastOutputTokens,
		LastError:          status.LastError,
		ConsecutiveErrors:  status.ConsecutiveErrors,
		HandlerOnly:        status.HandlerOnly,
		EventDriven:        status.EventDriven,
		LastSupervisorIter: status.LastSupervisorIter,
		ActiveTags:         append([]string(nil), status.ActiveTags...),
		Metadata:           cloneLoopMetadata(status.Config.Metadata),
	}
}

func cloneLoopMetadata(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
