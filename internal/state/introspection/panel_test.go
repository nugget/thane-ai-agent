package introspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func panelJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	start := strings.Index(body, "```json\n")
	end := strings.LastIndex(body, "\n```")
	if start < 0 || end < 0 {
		t.Fatalf("panel body carries no fenced JSON:\n%s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body[start+len("```json\n"):end]), &payload); err != nil {
		t.Fatalf("panel JSON does not parse: %v", err)
	}
	return payload
}

func TestPanelRendersPerceptionWithSummary(t *testing.T) {
	insp := NewInspector(HealthSources{
		ConnStatus: func() map[string]connwatch.ServiceStatus {
			return map[string]connwatch.ServiceStatus{
				"signal": {Name: "signal", Ready: false, LastError: "connection refused", LastCheck: time.Now()},
			}
		},
	})
	flagged := []documents.DocumentActivity{{Ref: "self:ego.md", Revisions: 14, Flagged: true, FlagReason: "14 revisions in the window meets the runaway threshold 8"}}
	p := NewPanelProvider(insp, func(context.Context) ([]documents.DocumentActivity, error) { return flagged, nil }, nil)

	if p.TagContextBucket() != agentctx.ContextBucketLiveState {
		t.Errorf("bucket = %v, want live state", p.TagContextBucket())
	}
	body, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.HasPrefix(body, "### Internal Operations Panel") {
		t.Errorf("panel missing its heading:\n%s", body)
	}
	payload := panelJSON(t, body)
	summary, _ := payload["summary"].(string)
	if !strings.Contains(summary, "conn:signal") {
		t.Errorf("summary = %q, want the failed connection named", summary)
	}
	docs, _ := payload["flagged_documents"].([]any)
	if len(docs) != 1 {
		t.Errorf("flagged_documents = %v, want the runaway ego row", payload["flagged_documents"])
	}
}

// TestPanelProbeFailureIsAFieldNotAnError pins the doctrine: a broken
// probe is a finding the panel surfaces, never a failed turn.
func TestPanelProbeFailureIsAFieldNotAnError(t *testing.T) {
	p := NewPanelProvider(NewInspector(HealthSources{}),
		func(context.Context) ([]documents.DocumentActivity, error) { return nil, errors.New("git exploded") }, nil)
	body, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext must not error: %v", err)
	}
	payload := panelJSON(t, body)
	if msg, _ := payload["flagged_documents_error"].(string); !strings.Contains(msg, "git exploded") {
		t.Errorf("probe failure not surfaced: %v", payload)
	}
}

// TestPanelElidesHealthyRowsWhenOversized: past the soft cap the panel
// keeps only the not-ok lamps, with an explicit marker pointing at
// system_health for the full set.
func TestPanelElidesHealthyRowsWhenOversized(t *testing.T) {
	many := make(map[string]connwatch.ServiceStatus, 400)
	for i := range 400 {
		name := fmt.Sprintf("svc-%03d-with-a-rather-long-descriptive-name", i)
		many[name] = connwatch.ServiceStatus{Name: name, Ready: true, LastCheck: time.Now()}
	}
	many["broken"] = connwatch.ServiceStatus{Name: "broken", Ready: false, LastError: "down"}
	p := NewPanelProvider(NewInspector(HealthSources{
		ConnStatus: func() map[string]connwatch.ServiceStatus { return many },
	}), nil, nil)

	body, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	payload := panelJSON(t, body)
	if _, marked := payload["annunciator_truncated"]; !marked {
		t.Fatalf("oversized panel not marked truncated (len=%d)", len(body))
	}
	rows, _ := payload["annunciator"].([]any)
	if len(rows) != 1 {
		t.Errorf("truncated annunciator = %d rows, want only the broken one", len(rows))
	}
}

func TestPanelNilInspectorRendersNothing(t *testing.T) {
	var p *PanelProvider
	if body, err := p.TagContext(context.Background(), agentctx.ContextRequest{}); err != nil || body != "" {
		t.Errorf("nil provider = (%q, %v), want empty and no error", body, err)
	}
}
