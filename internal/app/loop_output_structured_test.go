package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// fakeStructuredOutputSink records publishes instead of reaching a
// broker, so the tool-generation path is testable without MQTT.
type fakeStructuredOutputSink struct {
	entityID  string
	publishes []fakeStructuredPublish
	last      map[string]structuredOutputSnapshot
	err       error
}

type fakeStructuredPublish struct {
	binding structuredOutputBinding
	payload outputtargets.Payload
}

func (f *fakeStructuredOutputSink) Publish(_ context.Context, binding structuredOutputBinding, payload outputtargets.Payload) error {
	if f.err != nil {
		return f.err
	}
	f.publishes = append(f.publishes, fakeStructuredPublish{binding: binding, payload: payload})
	if f.last == nil {
		f.last = make(map[string]structuredOutputSnapshot)
	}
	f.last[binding.EntitySuffix] = structuredOutputSnapshot{
		Payload: payload,
		At:      time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	}
	return nil
}

func (f *fakeStructuredOutputSink) EntityID(entitySuffix string) string {
	if f.entityID != "" {
		return f.entityID
	}
	return "sensor.thane_" + entitySuffix
}

func (f *fakeStructuredOutputSink) Last(entitySuffix string) (structuredOutputSnapshot, bool) {
	snapshot, ok := f.last[entitySuffix]
	return snapshot, ok
}

func structuredOutputSpec() looppkg.OutputSpec {
	return looppkg.OutputSpec{
		Name:    "watch_status",
		Type:    looppkg.OutputTypeStructuredPayload,
		Ref:     "mqtt:watch_status",
		Target:  "apple_watch.rectangular",
		Purpose: "Household status on the watch face.",
	}
}

func TestBuildStructuredOutputToolAdvertisesTheSlotContract(t *testing.T) {
	t.Parallel()

	sink := &fakeStructuredOutputSink{}
	tool, err := buildStructuredOutputTool(sink, structuredOutputSpec())
	if err != nil {
		t.Fatalf("buildStructuredOutputTool: %v", err)
	}

	if tool.Name != "set_output_watch_status" {
		t.Fatalf("tool name = %q", tool.Name)
	}
	if !tool.SkipContentResolve {
		t.Fatal("slot text must not be run through content resolution")
	}
	for _, want := range []string{"watch_status", "sensor.thane_watch_status", "Apple Watch rectangular"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("description missing %q: %s", want, tool.Description)
		}
	}

	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters have no properties: %#v", tool.Parameters)
	}
	for _, slot := range []string{"value", "title", "subtitle", "bottom_text", "fraction", "gauge_color"} {
		if _, present := properties[slot]; !present {
			t.Errorf("schema is missing slot %q", slot)
		}
	}
}

func TestStructuredOutputToolPublishesNormalizedPayload(t *testing.T) {
	t.Parallel()

	sink := &fakeStructuredOutputSink{}
	tool, err := buildStructuredOutputTool(sink, structuredOutputSpec())
	if err != nil {
		t.Fatalf("buildStructuredOutputTool: %v", err)
	}

	result, err := tool.Handler(context.Background(), map[string]any{
		"value":       " 64% ",
		"title":       "Battery",
		"fraction":    0.64,
		"gauge_color": "3fb950",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if len(sink.publishes) != 1 {
		t.Fatalf("publishes = %d, want 1", len(sink.publishes))
	}
	published := sink.publishes[0]
	if published.binding.EntitySuffix != "watch_status" {
		t.Fatalf("entity suffix = %q", published.binding.EntitySuffix)
	}
	if published.binding.Label != "watch_status" {
		t.Fatalf("label = %q", published.binding.Label)
	}
	if published.payload.State != "64%" {
		t.Fatalf("state = %q, want trimmed %q", published.payload.State, "64%")
	}
	if published.payload.Attributes["gauge_color"] != "#3FB950" {
		t.Fatalf("gauge_color = %v, want canonicalized", published.payload.Attributes["gauge_color"])
	}

	var decoded structuredOutputToolResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if decoded.EntityID != "sensor.thane_watch_status" {
		t.Fatalf("result entity_id = %q", decoded.EntityID)
	}
	if decoded.Target != "apple_watch.rectangular" {
		t.Fatalf("result target = %q", decoded.Target)
	}
	if decoded.State != "64%" {
		t.Fatalf("result state = %q", decoded.State)
	}
}

func TestStructuredOutputToolRejectsBeforePublishing(t *testing.T) {
	t.Parallel()

	sink := &fakeStructuredOutputSink{}
	tool, err := buildStructuredOutputTool(sink, structuredOutputSpec())
	if err != nil {
		t.Fatalf("buildStructuredOutputTool: %v", err)
	}

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing required slot", args: map[string]any{"title": "Battery"}, want: `slot "value" is required`},
		{name: "over budget", args: map[string]any{"value": "1234567890123"}, want: "at most 12"},
		{name: "unknown slot", args: map[string]any{"value": "64%", "icon": "mdi:battery"}, want: "no slot named"},
		{name: "fraction out of range", args: map[string]any{"value": "64%", "fraction": 64}, want: "between 0.0 and 1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tool.Handler(context.Background(), tt.args); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}

	if len(sink.publishes) != 0 {
		t.Fatalf("a rejected payload reached the sink: %d publishes", len(sink.publishes))
	}
}

func TestStructuredOutputToolSurfacesSinkFailure(t *testing.T) {
	t.Parallel()

	sink := &fakeStructuredOutputSink{err: fmt.Errorf("broker unreachable")}
	tool, err := buildStructuredOutputTool(sink, structuredOutputSpec())
	if err != nil {
		t.Fatalf("buildStructuredOutputTool: %v", err)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"value": "64%"}); err == nil {
		t.Fatal("expected the sink failure to reach the model")
	} else if !strings.Contains(err.Error(), "broker unreachable") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildStructuredOutputToolRequiresSinkAndTarget(t *testing.T) {
	t.Parallel()

	if _, err := buildStructuredOutputTool(nil, structuredOutputSpec()); err == nil {
		t.Fatal("expected an error without a sink")
	}

	unknown := structuredOutputSpec()
	unknown.Target = "apple_watch.trapezoid"
	_, err := buildStructuredOutputTool(&fakeStructuredOutputSink{}, unknown)
	if err == nil {
		t.Fatal("expected an error for an unknown target")
	}
	if !strings.Contains(err.Error(), "registered targets are") {
		t.Fatalf("error should enumerate the valid targets: %v", err)
	}
}

func TestHydrateLoopOutputsRequiresMQTTForStructuredPayloads(t *testing.T) {
	t.Parallel()

	app := &App{cfg: &config.Config{}}
	spec := looppkg.Spec{
		Name:       "watchface",
		Enabled:    true,
		Task:       "Drive the watch face.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs:    []looppkg.OutputSpec{structuredOutputSpec()},
	}

	_, err := app.hydrateLoopOutputs(spec)
	if err == nil {
		t.Fatal("expected hydration to fail without MQTT configured")
	}
	if !strings.Contains(err.Error(), "MQTT publishing is not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestStructuredOutputContextTeachesTheContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	sink := &fakeStructuredOutputSink{}
	output := structuredOutputSpec()

	rendered, err := renderLoopOutputContextWithNow(context.Background(), nil, sink, []looppkg.OutputSpec{output}, now)
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow: %v", err)
	}
	for _, want := range []string{
		`"tool_name": "set_output_watch_status"`,
		`"id": "apple_watch.rectangular"`,
		`"entity_id": "sensor.thane_watch_status"`,
		`"max_runes": 12`,
		"nothing published from this process yet",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("context missing %q:\n%s", want, rendered)
		}
	}

	// After a publish the context should show what landed and how long
	// ago, so the next iteration can decide whether to rewrite.
	if err := sink.Publish(context.Background(), structuredOutputBinding{EntitySuffix: "watch_status"}, outputtargets.Payload{
		State:      "64%",
		Attributes: map[string]any{"title": "Battery"},
	}); err != nil {
		t.Fatalf("seed publish: %v", err)
	}

	rendered, err = renderLoopOutputContextWithNow(context.Background(), nil, sink, []looppkg.OutputSpec{output}, now)
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow: %v", err)
	}
	for _, want := range []string{`"state": "64%"`, `"last_published_delta": "-2h"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("context missing %q:\n%s", want, rendered)
		}
	}
}
