package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

func parseCostSummaryOptions(ctx context.Context, args map[string]any, now time.Time) (usage.ReportOptions, string, error) {
	var problems []string
	readString := func(key string, maxRunes int) string {
		raw, present := args[key]
		if !present {
			return ""
		}
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			problems = append(problems, key+" must be a nonempty string or omitted")
			return ""
		}
		value = strings.TrimSpace(value)
		if utf8.RuneCountInString(value) > maxRunes {
			problems = append(problems, fmt.Sprintf("%s exceeds %d characters; shorten it", key, maxRunes))
			return ""
		}
		return value
	}
	period := strings.ToLower(readString("period", 16))
	since, until := readString("since", 128), readString("until", 128)
	opts := usage.ReportOptions{
		LoopID: readString("loop_id", 256), LoopName: readString("loop_name", 256),
		GroupBy: strings.ToLower(readString("group_by", 32)), Limit: 20, End: now,
	}
	if opts.LoopID != "" && opts.LoopName != "" {
		problems = append(problems, "use only one of loop_id or loop_name")
	}
	if opts.LoopID == "self" {
		opts.LoopID = LoopIDFromContext(ctx)
		if opts.LoopID == "" {
			problems = append(problems, "loop_id=self requires a calling loop; provide an exact historical loop_id or omit the selector for global usage")
		}
	}
	if opts.GroupBy == "" && (opts.LoopID != "" || opts.LoopName != "") {
		opts.GroupBy = "loop"
	}
	switch opts.GroupBy {
	case "", "loop", "deployment", "model", "upstream_model", "provider", "resource", "role", "task":
	default:
		problems = append(problems, "group_by must be loop, deployment, model, upstream_model, provider, resource, role, or task")
	}
	if _, present := args["limit"]; present {
		limit, ok := toolargs.IntOK(args, "limit")
		if !ok || limit < 1 || limit > 100 {
			problems = append(problems, "limit must be an integer from 1 to 100")
		} else {
			opts.Limit = limit
		}
	}
	_, hasPeriod := args["period"]
	_, hasSince := args["since"]
	_, hasUntil := args["until"]
	if hasPeriod && (hasSince || hasUntil) {
		problems = append(problems, "use period or since/until, not both")
	}
	if hasUntil && !hasSince {
		problems = append(problems, "until requires since to define a custom time window")
	}
	if hasSince || hasUntil {
		for _, field := range []struct {
			key, value string
			target     *time.Time
		}{{"since", since, &opts.Start}, {"until", until, &opts.End}} {
			if field.value == "" {
				continue
			}
			parsed, err := promptfmt.ParseTimeOrDelta(field.value, now)
			if err != nil {
				problems = append(problems, field.key+" must be a signed offset (such as -24h) or an RFC3339 timestamp")
			} else {
				*field.target = parsed
			}
		}
	} else {
		if period == "" {
			period = "today"
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		switch period {
		case "today":
			opts.Start = today
		case "yesterday":
			opts.Start, opts.End = today.AddDate(0, 0, -1), today
		case "week":
			opts.Start = now.AddDate(0, 0, -7)
		case "month":
			opts.Start = now.AddDate(0, -1, 0)
		case "all":
		default:
			problems = append(problems, "period must be today, yesterday, week, month, or all")
		}
	}
	if !opts.Start.Before(opts.End) {
		problems = append(problems, "since must precede until; choose a nonempty time window")
	}
	if len(problems) > 0 {
		return usage.ReportOptions{}, "", fmt.Errorf("cost_summary: %s", strings.Join(problems, "; "))
	}
	return opts, period, nil
}
