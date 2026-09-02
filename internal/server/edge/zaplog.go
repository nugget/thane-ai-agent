package edge

import (
	"context"
	"log/slog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newZapBridge returns a zap logger that forwards to slog. certmagic and
// its ACME client log through zap; Thane's operator story is told in
// slog. The bridge maps levels one to one, keeps structured fields, and
// drops debug chatter unless the slog handler itself wants it, so the
// second logging stack stays invisible.
func newZapBridge(logger *slog.Logger) *zap.Logger {
	return zap.New(&slogCore{logger: logger})
}

// slogCore is the zapcore.Core that does the forwarding.
type slogCore struct {
	logger *slog.Logger
	fields []zapcore.Field
}

func (c *slogCore) Enabled(level zapcore.Level) bool {
	return c.logger.Enabled(context.Background(), zapToSlogLevel(level))
}

func (c *slogCore) With(fields []zapcore.Field) zapcore.Core {
	merged := make([]zapcore.Field, 0, len(c.fields)+len(fields))
	merged = append(merged, c.fields...)
	merged = append(merged, fields...)
	return &slogCore{logger: c.logger, fields: merged}
}

func (c *slogCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *slogCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	attrs := make([]any, 0, 2*len(enc.Fields)+2)
	attrs = append(attrs, "subsystem", "tls")
	for k, v := range enc.Fields {
		attrs = append(attrs, k, v)
	}
	if entry.LoggerName != "" {
		attrs = append(attrs, "logger", entry.LoggerName)
	}
	c.logger.Log(context.Background(), zapToSlogLevel(entry.Level), entry.Message, attrs...)
	return nil
}

func (c *slogCore) Sync() error { return nil }

// zapToSlogLevel maps zap's levels onto slog's four. Everything at or
// above error collapses to slog.LevelError: a panic-level entry from a
// library is still just an error to the operator.
func zapToSlogLevel(level zapcore.Level) slog.Level {
	switch {
	case level <= zapcore.DebugLevel:
		return slog.LevelDebug
	case level == zapcore.InfoLevel:
		return slog.LevelInfo
	case level == zapcore.WarnLevel:
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}
