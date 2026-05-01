package messages

import (
	"context"
	"log/slog"
	"time"
)

const defaultActivityIndicatorInterval = 10 * time.Second

// ActivityIndicator manages a short-lived channel activity marker, such as
// a typing indicator. It sends one marker immediately, refreshes it on an
// interval, and lets callers send a final stop marker with a fresh context.
type ActivityIndicator struct {
	// Name is included in debug logs so transport-specific failures can be
	// identified without making this helper depend on a particular channel.
	Name string

	// Interval controls how often Refresh is called after the initial
	// marker. Zero uses the default interval.
	Interval time.Duration

	// Start sends the first activity marker. When nil, Refresh is used.
	Start func(context.Context) error

	// Refresh renews the marker while work is still in progress. When nil,
	// Start is used for refreshes too.
	Refresh func(context.Context) error

	// Stop clears the marker after work is complete. Nil means there is no
	// explicit stop action for this transport.
	Stop func(context.Context) error

	// Logger receives debug-level start, refresh, and stop failures.
	Logger *slog.Logger
}

// Begin starts the activity marker and returns a cancel function that stops
// future refreshes. It does not call Stop; callers should use End with a
// context appropriate for cleanup after cancelling refreshes.
func (i ActivityIndicator) Begin(ctx context.Context) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := i.logger()
	refreshCtx, cancel := context.WithCancel(ctx)

	start := i.Start
	if start == nil {
		start = i.Refresh
	}
	if start != nil {
		if err := start(refreshCtx); err != nil {
			logger.Debug("activity indicator start failed",
				"indicator", i.name(),
				"error", err,
			)
		}
	}

	refresh := i.Refresh
	if refresh == nil {
		refresh = i.Start
	}
	if refresh == nil {
		return cancel
	}

	interval := i.Interval
	if interval <= 0 {
		interval = defaultActivityIndicatorInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				if err := refresh(refreshCtx); err != nil {
					logger.Debug("activity indicator refresh failed",
						"indicator", i.name(),
						"error", err,
					)
				}
			}
		}
	}()

	return cancel
}

// End sends the final stop marker when the transport supports one.
func (i ActivityIndicator) End(ctx context.Context) {
	if i.Stop == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := i.Stop(ctx); err != nil {
		i.logger().Debug("activity indicator stop failed",
			"indicator", i.name(),
			"error", err,
		)
	}
}

func (i ActivityIndicator) logger() *slog.Logger {
	if i.Logger != nil {
		return i.Logger
	}
	return slog.Default()
}

func (i ActivityIndicator) name() string {
	if i.Name != "" {
		return i.Name
	}
	return "activity_indicator"
}
