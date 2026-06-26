package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

const (
	defaultLoopStatusLimit = 25
	maxLoopStatusLimit     = 200
)

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
		Description: "Inspect the live loop registry. Returns a rich canonical row per running loop — identity, parent container and ancestry, lifecycle counters (successful iterations vs attempts), token economics, state and policy, supervisor cadence, and effective tags/subscriptions with inheritance provenance — with optional filters by query text, state, operation, and a result limit.",
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
					"enum":        []string{"request_reply", "background_task", "service", "container", "event_driven"},
					"description": "Optional exact operation filter. Use container to list only grouping nodes.",
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
		Name:        "set_next_sleep",
		Description: "Request the next sleep duration for the current running timer-driven service loop. This is the native loops sleep-control tool used by persistent background services such as metacog-style loops. The requested duration is clamped to the loop's configured sleep_min and sleep_max bounds.",
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

	spawnLoopLaunchProperties := loopLaunchOverrideProperties()
	spawnLoopLaunchProperties["spec"] = loopSpecSchema("Ad-hoc loop spec (name, task, operation, completion, sleep settings, etc.). Required.")

	r.Register(&Tool{
		Name:        "spawn_loop",
		Description: "Launch an ad hoc loop immediately using the loops launch contract without persisting a loop definition. Use this for temporary services, detached background research that should report back later, or one-shot request/reply runs that do not belong in the durable loop-definition registry. For async work, prefer operation=background_task. If completion is omitted there, the runtime infers the most natural detached delivery target from the current origin context. Tool filtering goes in the top-level launch fields (allowed_tools, etc.) — NOT inside launch.metadata. To pin a model, set spec.profile.model inside the spec object; per-launch model overrides are rejected.",
		// The launch carries a declarative spec; its strings (notably
		// spec.outputs[].ref) must be stored verbatim, not expanded.
		// Universal prefix-to-content resolution would otherwise rewrite a
		// real ref into document content, and the ad-hoc loop would die at
		// its first wake with `unknown document root`. See #1068.
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"launch": map[string]any{
					"type":        "object",
					"description": "Loops-ng launch object. Must include spec. The most common detached shape is a task plus spec.operation=background_task; completion may be omitted when the current conversation or channel context should decide the callback target.",
					"properties":  spawnLoopLaunchProperties,
					"required":    []string{"spec"},
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
	query := strings.ToLower(toolargs.TrimmedString(args, "query"))
	state := strings.ToLower(toolargs.TrimmedString(args, "state"))
	operation := looppkg.Operation(toolargs.TrimmedString(args, "operation"))
	limit := clampLoopListLimit(toolargs.Int(args, "limit"), defaultLoopStatusLimit, maxLoopStatusLimit)

	statuses := r.liveLoopRegistry.Statuses()
	// One resolver over the FULL status set (graph joins built once, not
	// per row) plus the definition-policy join, so every row is the canonical
	// LoopView — the same rich shape every loop-data tool emits.
	resolver := looppkg.NewLoopViewResolver(statuses, r.loopPolicyByName(), time.Now())
	filtered := make([]looppkg.LoopView, 0, len(statuses))
	for _, status := range statuses {
		// Project first so the query can match the human parent_name/ancestry
		// the row now surfaces, not just the opaque parent_id.
		view := resolver.FromStatus(status)
		if !matchLoopStatus(status, view, query, state, operation) {
			continue
		}
		filtered = append(filtered, view)
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
		// Health is computed over the FULL registry, not the filtered/limited
		// rows, so "is anything wrong" is answerable in one read even when a
		// degraded loop falls below the result limit.
		"health": loopStatusHealth(statuses),
		"loops":  filtered,
	})
}

// maxDegradedLoopNames caps the degraded_loops list to keep the health
// rollup's prompt footprint bounded; the count stays exact.
const maxDegradedLoopNames = 10

// loopStatusHealth summarizes the live registry: counts by state and the
// loops that are degraded (in error, or carrying consecutive errors), so the
// model can read "is anything wrong" without eyeballing every row. statuses
// is already name-sorted (Registry.Statuses), so degraded_loops is
// deterministic without an explicit sort.
func loopStatusHealth(statuses []looppkg.Status) map[string]any {
	byState := make(map[string]int)
	degraded := make([]string, 0)
	for _, s := range statuses {
		byState[string(s.State)]++
		switch {
		case s.ConsecutiveErrors > 0:
			degraded = append(degraded, fmt.Sprintf("%s (%d consecutive errors)", s.Name, s.ConsecutiveErrors))
		case s.State == looppkg.StateError:
			// Errored this cycle but the counter already reset (or never
			// incremented) — label the state, not a misleading "0 errors".
			degraded = append(degraded, fmt.Sprintf("%s (error)", s.Name))
		}
	}
	degradedCount := len(degraded)
	if len(degraded) > maxDegradedLoopNames {
		degraded = degraded[:maxDegradedLoopNames]
	}
	health := map[string]any{
		"total":          len(statuses),
		"degraded":       degradedCount,
		"by_state":       byState,
		"degraded_loops": degraded,
	}
	if degradedCount > len(degraded) {
		health["degraded_truncated"] = degradedCount - len(degraded)
	}
	return health
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
	reason := toolargs.TrimmedString(args, "reason")
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
		durStr = strconv.FormatFloat(float64(v), 'f', -1, 64) + "m"
	case float64:
		durStr = strconv.FormatFloat(v, 'f', -1, 64) + "m"
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
	loopID := toolargs.TrimmedString(args, "loop_id")
	name := toolargs.TrimmedString(args, "name")
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

func matchLoopStatus(status looppkg.Status, view looppkg.LoopView, query, state string, operation looppkg.Operation) bool {
	if state != "" && strings.ToLower(string(status.State)) != state {
		return false
	}
	if operation != "" && !matchLoopOperation(status, operation) {
		return false
	}
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(status.ID), query) || strings.Contains(strings.ToLower(status.Name), query) {
		return true
	}
	// Match the human parent/ancestry names the row surfaces, so searching a
	// container name (e.g. "travel") finds its descendants — the opaque
	// parent_id is no longer the searchable handle.
	if view.ParentName != nil && strings.Contains(strings.ToLower(*view.ParentName), query) {
		return true
	}
	for _, anc := range view.Ancestry {
		if strings.Contains(strings.ToLower(anc), query) {
			return true
		}
	}
	for _, value := range status.Config.Metadata {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

// matchLoopOperation matches the operation filter. event_driven is inclusive
// of WaitFunc-based loops (Status.EventDriven=true) whose declared operation
// is still service, so the filter catches every event-driven loop, not only
// those whose Config.Operation literally equals event_driven.
func matchLoopOperation(status looppkg.Status, operation looppkg.Operation) bool {
	if operation == looppkg.OperationEventDriven {
		return status.EventDriven || status.Config.Operation == looppkg.OperationEventDriven
	}
	return status.Config.Operation == operation
}

// loopPolicyByName builds the name→policy/eligibility join the LoopView
// projector left-joins onto live loops, so each row shows active/paused and
// eligibility without a second tool call. Returns nil when no definition
// view is wired (loops then report policy_state="ephemeral").
func (r *Registry) loopPolicyByName() map[string]looppkg.LoopPolicyInfo {
	if r.loopDefinitionView == nil {
		return nil
	}
	view := r.loopDefinitionView()
	if view == nil {
		return nil
	}
	out := make(map[string]looppkg.LoopPolicyInfo, len(view.Definitions))
	for _, def := range view.Definitions {
		out[def.Name] = looppkg.LoopPolicyInfo{
			State:          string(def.PolicyState),
			Source:         string(def.PolicySource),
			Reason:         def.PolicyReason,
			UpdatedAt:      def.PolicyUpdatedAt,
			Eligible:       def.Eligibility.Eligible,
			EligibleReason: def.Eligibility.Reason,
			HasPolicy:      true,
		}
	}
	return out
}
