package loop

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// OutputTargetKind describes one concrete non-conversation output path
// for loop observations.
type OutputTargetKind string

const (
	// OutputTargetObservationLog writes structured observations into the
	// persistent database log for later inspection.
	OutputTargetObservationLog OutputTargetKind = "observation_log"
	// OutputTargetDocumentJournal appends timestamped observations into a
	// managed document journal.
	OutputTargetDocumentJournal OutputTargetKind = "document_journal"
	// OutputTargetMQTTTopic publishes structured observation payloads to
	// an MQTT topic.
	OutputTargetMQTTTopic OutputTargetKind = "mqtt_topic"
)

// OutputTarget configures one concrete observation destination for a loop.
// The top-level shape is intentionally flat so model-authored specs do not
// need nested transport-specific payloads for simple cases.
type OutputTarget struct {
	Kind        OutputTargetKind `yaml:"kind,omitempty" json:"kind,omitempty"`
	Ref         string           `yaml:"ref,omitempty" json:"ref,omitempty"`
	Topic       string           `yaml:"topic,omitempty" json:"topic,omitempty"`
	Retain      bool             `yaml:"retain,omitempty" json:"retain,omitempty"`
	Window      string           `yaml:"window,omitempty" json:"window,omitempty"`
	MaxWindows  int              `yaml:"max_windows,omitempty" json:"max_windows,omitempty"`
	Title       string           `yaml:"title,omitempty" json:"title,omitempty"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        []string         `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Validate checks that the target is internally consistent.
func (t OutputTarget) Validate() error {
	switch t.Kind {
	case OutputTargetObservationLog:
		return nil
	case OutputTargetDocumentJournal:
		if strings.TrimSpace(t.Ref) == "" {
			return fmt.Errorf("loop: document_journal output target requires ref")
		}
		if t.MaxWindows < 0 {
			return fmt.Errorf("loop: document_journal output target max_windows must be >= 0")
		}
		return nil
	case OutputTargetMQTTTopic:
		if strings.TrimSpace(t.Topic) == "" {
			return fmt.Errorf("loop: mqtt_topic output target requires topic")
		}
		return nil
	case "":
		return fmt.Errorf("loop: output target kind is required")
	default:
		return fmt.Errorf("loop: unsupported output target kind %q", t.Kind)
	}
}

func cloneOutputTargets(src []OutputTarget) []OutputTarget {
	if len(src) == 0 {
		return nil
	}
	dst := make([]OutputTarget, len(src))
	copy(dst, src)
	for i := range dst {
		if len(src[i].Tags) > 0 {
			dst[i].Tags = append([]string(nil), src[i].Tags...)
		}
	}
	return dst
}

// Observation is the normalized payload emitted from one successful loop
// iteration before it is dispatched to concrete output targets.
type Observation struct {
	Timestamp      time.Time         `yaml:"timestamp,omitempty" json:"timestamp,omitempty"`
	LoopID         string            `yaml:"loop_id,omitempty" json:"loop_id,omitempty"`
	LoopName       string            `yaml:"loop_name,omitempty" json:"loop_name,omitempty"`
	Operation      Operation         `yaml:"operation,omitempty" json:"operation,omitempty"`
	Iteration      int               `yaml:"iteration,omitempty" json:"iteration,omitempty"`
	ConversationID string            `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	RequestID      string            `yaml:"request_id,omitempty" json:"request_id,omitempty"`
	Model          string            `yaml:"model,omitempty" json:"model,omitempty"`
	Content        string            `yaml:"content,omitempty" json:"content,omitempty"`
	Summary        map[string]any    `yaml:"summary,omitempty" json:"summary,omitempty"`
	InputTokens    int               `yaml:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens   int               `yaml:"output_tokens,omitempty" json:"output_tokens,omitempty"`
	Supervisor     bool              `yaml:"supervisor,omitempty" json:"supervisor,omitempty"`
	Metadata       map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	ActiveTags     []string          `yaml:"active_tags,omitempty" json:"active_tags,omitempty"`
}

func cloneObservation(src Observation) Observation {
	out := src
	if len(src.Summary) > 0 {
		out.Summary = make(map[string]any, len(src.Summary))
		for k, v := range src.Summary {
			out.Summary[k] = v
		}
	}
	if len(src.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(src.Metadata))
		for k, v := range src.Metadata {
			out.Metadata[k] = v
		}
	}
	if len(src.ActiveTags) > 0 {
		out.ActiveTags = append([]string(nil), src.ActiveTags...)
	}
	return out
}

// OutputDelivery is the normalized observation dispatch payload handed to
// the app layer for concrete delivery.
type OutputDelivery struct {
	Targets     []OutputTarget `yaml:"targets,omitempty" json:"targets,omitempty"`
	Observation Observation    `yaml:"observation,omitempty" json:"observation"`
}

// OutputSink receives loop observation deliveries. The loop package stays
// free of document, MQTT, and storage dependencies by delegating concrete
// delivery to the app layer.
type OutputSink func(ctx context.Context, delivery OutputDelivery) error

func cloneStringAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func observationContentForIteration(result *IterationResult, summary map[string]any, resp *Response) string {
	if resp != nil && strings.TrimSpace(resp.Content) != "" {
		return strings.TrimSpace(resp.Content)
	}
	if result == nil || len(summary) == 0 {
		return ""
	}
	if content, ok := summary["observation"].(string); ok {
		return strings.TrimSpace(content)
	}
	return ""
}

func (l *Loop) dispatchOutputsAsync(observation Observation) {
	if l == nil || len(l.config.Outputs) == 0 || l.deps.OutputSink == nil {
		return
	}
	delivery := OutputDelivery{
		Targets:     cloneOutputTargets(l.config.Outputs),
		Observation: cloneObservation(observation),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), detachedOutputTimeout)
		defer cancel()
		if err := l.deps.OutputSink(ctx, delivery); err != nil {
			logger := l.deps.Logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("loop output delivery failed",
				"loop_id", l.id,
				"loop_name", l.config.Name,
				"targets", len(delivery.Targets),
				"error", err,
			)
		}
	}()
}
