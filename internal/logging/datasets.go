package logging

import (
	"context"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"time"
)

// DatasetHandlerOptions controls how slog records are split between filesystem
// datasets and operator-facing stdout.
type DatasetHandlerOptions struct {
	DatasetLevel    slog.Level
	StdoutLevel     slog.Level
	StdoutEnabled   bool
	EventsEnabled   bool
	RequestsEnabled bool
	AccessEnabled   bool
}

// DatasetHandler routes slog records into structured JSONL datasets and a
// separately filtered stdout handler.
type DatasetHandler struct {
	inner    slog.Handler
	writer   *DatasetWriter
	options  DatasetHandlerOptions
	preAttrs []slog.Attr
	groups   []string
}

// NewDatasetHandler creates a handler that writes structured dataset records
// and forwards only operator-significant records to stdout.
func NewDatasetHandler(inner slog.Handler, writer *DatasetWriter, options DatasetHandlerOptions) *DatasetHandler {
	return &DatasetHandler{
		inner:   inner,
		writer:  writer,
		options: options,
	}
}

// Enabled reports whether either stdout or dataset retention needs this level.
func (h *DatasetHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.inner != nil && h.options.StdoutEnabled && h.inner.Enabled(ctx, level) {
		return true
	}
	return h.writer != nil && level >= h.options.DatasetLevel
}

// Handle routes one slog record into the configured dataset stream and stdout.
func (h *DatasetHandler) Handle(ctx context.Context, r slog.Record) error {
	projection := h.projectRecord(r)
	decision := classifyDataset(projection)

	if h.writer != nil && r.Level >= h.options.DatasetLevel && datasetWriteEnabled(h.options, decision.Dataset) {
		if err := h.writer.WriteRecord(projection.toDatasetRecord(decision)); err != nil {
			return err
		}
	}

	if h.inner != nil && h.options.StdoutEnabled && stdoutShouldEmit(h.options, decision.Dataset, r.Level) && h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

// WithAttrs returns a derived handler with attrs applied to both sinks.
func (h *DatasetHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	derived := &DatasetHandler{
		inner:    nil,
		writer:   h.writer,
		options:  h.options,
		preAttrs: append(cloneAttrs(h.preAttrs), attrs...),
		groups:   h.groups,
	}
	if h.inner != nil {
		derived.inner = h.inner.WithAttrs(attrs)
	}
	return derived
}

// WithGroup returns a derived handler with the given group.
func (h *DatasetHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	derived := &DatasetHandler{
		inner:    nil,
		writer:   h.writer,
		options:  h.options,
		preAttrs: cloneAttrs(h.preAttrs),
		groups:   append(slices.Clone(h.groups), name),
	}
	if h.inner != nil {
		derived.inner = h.inner.WithGroup(name)
	}
	return derived
}

type datasetProjection struct {
	Timestamp      time.Time
	Severity       string
	Message        string
	Kind           string
	RequestID      string
	SessionID      string
	ConversationID string
	LoopID         string
	LoopName       string
	DelegateID     string
	Subsystem      string
	Component      string
	SourceFile     string
	SourceLine     int
	Attrs          map[string]any
}

type datasetDecision struct {
	Dataset string
	Kind    string
	Source  string
}

func (h *DatasetHandler) projectRecord(r slog.Record) datasetProjection {
	projection := datasetProjection{
		Timestamp: r.Time,
		Severity:  normalizeLevel(r.Level),
		Message:   r.Message,
		Attrs:     make(map[string]any),
	}
	if r.PC != 0 {
		frames := runtime.CallersFrames([]uintptr{r.PC})
		frame, _ := frames.Next()
		projection.SourceFile = strings.TrimPrefix(frame.File, modulePrefix)
		projection.SourceLine = frame.Line
	}

	for _, attr := range h.preAttrs {
		classifyDatasetAttr(attr, &projection, h.groups)
	}
	r.Attrs(func(attr slog.Attr) bool {
		classifyDatasetAttr(attr, &projection, h.groups)
		return true
	})

	if len(projection.Attrs) == 0 {
		projection.Attrs = nil
	}
	return projection
}

func classifyDatasetAttr(attr slog.Attr, projection *datasetProjection, groups []string) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	key := attr.Key
	if attr.Value.Kind() == slog.KindGroup {
		childGroups := groups
		if key != "" {
			childGroups = append(slices.Clone(groups), key)
		}
		for _, child := range attr.Value.Group() {
			classifyDatasetAttr(child, projection, childGroups)
		}
		return
	}

	qualifiedKey := key
	if len(groups) > 0 {
		qualifiedKey = strings.Join(groups, ".") + "." + key
	}

	if len(groups) == 0 {
		value := attr.Value.String()
		switch key {
		case "request_id":
			projection.RequestID = value
			return
		case "session_id":
			projection.SessionID = value
			return
		case "conversation_id":
			projection.ConversationID = value
			return
		case "loop_id":
			projection.LoopID = value
			return
		case "loop_name":
			projection.LoopName = value
			return
		case "delegate_id":
			projection.DelegateID = value
			return
		case "subsystem":
			projection.Subsystem = value
			return
		case "component":
			projection.Component = value
			return
		case "kind":
			projection.Kind = value
			return
		}
	}

	switch key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey, slog.SourceKey:
		return
	}
	projection.Attrs[qualifiedKey] = attrValue(attr)
}

func classifyDataset(projection datasetProjection) datasetDecision {
	kind := strings.TrimSpace(projection.Kind)
	if kind == "" {
		kind = normalizeDatasetKind(projection.Message)
	}
	source := datasetSource(projection)

	switch {
	case kind == "http_access" || kind == "request_received":
		return datasetDecision{Dataset: DatasetAccess, Kind: kind, Source: source}
	case projection.Subsystem == SubsystemAgent && projection.RequestID != "":
		return datasetDecision{Dataset: DatasetRequests, Kind: kind, Source: source}
	case projection.Subsystem == SubsystemLoop || projection.Subsystem == SubsystemDelegate || projection.Component == "message_bus":
		return datasetDecision{Dataset: "", Kind: kind, Source: source}
	default:
		return datasetDecision{Dataset: DatasetEvents, Kind: kind, Source: source}
	}
}

func datasetSource(projection datasetProjection) string {
	switch {
	case projection.Component != "":
		return projection.Component
	case projection.Subsystem != "":
		return projection.Subsystem
	default:
		return "runtime"
	}
}

func datasetWriteEnabled(options DatasetHandlerOptions, dataset string) bool {
	switch dataset {
	case DatasetEvents:
		return options.EventsEnabled
	case DatasetRequests:
		return options.RequestsEnabled
	case DatasetAccess:
		return options.AccessEnabled
	default:
		return false
	}
}

func stdoutShouldEmit(options DatasetHandlerOptions, dataset string, level slog.Level) bool {
	if !options.StdoutEnabled {
		return false
	}
	if level < options.StdoutLevel {
		return false
	}
	if level >= slog.LevelWarn {
		return true
	}
	return dataset == DatasetEvents
}

func normalizeDatasetKind(message string) string {
	message = strings.TrimSpace(strings.ToLower(message))
	if message == "" {
		return "log"
	}
	var b strings.Builder
	underscore := false
	for _, r := range message {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			underscore = false
		default:
			if !underscore {
				b.WriteByte('_')
				underscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func (p datasetProjection) toDatasetRecord(decision datasetDecision) DatasetRecord {
	payload := make(map[string]any, len(p.Attrs)+4)
	payload["message"] = p.Message
	if p.SourceFile != "" {
		payload["source_file"] = p.SourceFile
		payload["source_line"] = p.SourceLine
	}
	for key, value := range p.Attrs {
		payload[key] = value
	}

	return DatasetRecord{
		Timestamp:      p.Timestamp.UTC(),
		Dataset:        decision.Dataset,
		Kind:           decision.Kind,
		SchemaVersion:  1,
		RequestID:      p.RequestID,
		SessionID:      p.SessionID,
		ConversationID: p.ConversationID,
		LoopID:         p.LoopID,
		DelegateID:     p.DelegateID,
		Source:         decision.Source,
		Severity:       p.Severity,
		Payload:        payload,
	}
}
