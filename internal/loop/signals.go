package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/messages"
)

type pendingSignal struct {
	Envelope        messages.Envelope
	ForceSupervisor bool
}

// SignalReceipt summarizes the effect of signaling a live loop.
type SignalReceipt struct {
	LoopID            string `json:"loop_id"`
	LoopName          string `json:"loop_name"`
	State             State  `json:"state"`
	WokeImmediately   bool   `json:"woke_immediately,omitempty"`
	QueuedForNextWake bool   `json:"queued_for_next_wake,omitempty"`
	ForceSupervisor   bool   `json:"force_supervisor,omitempty"`
	PendingSignals    int    `json:"pending_signals,omitempty"`
}

type signalContextKey struct{}

// SignalEnvelopesFromContext returns one-shot message envelopes delivered to
// the current loop iteration, if any.
func SignalEnvelopesFromContext(ctx context.Context) []messages.Envelope {
	envs, _ := ctx.Value(signalContextKey{}).([]messages.Envelope)
	if len(envs) == 0 {
		return nil
	}
	out := make([]messages.Envelope, len(envs))
	copy(out, envs)
	return out
}

func withSignalEnvelopes(ctx context.Context, envs []messages.Envelope) context.Context {
	if len(envs) == 0 {
		return ctx
	}
	cp := make([]messages.Envelope, len(envs))
	copy(cp, envs)
	return context.WithValue(ctx, signalContextKey{}, cp)
}

func decodeLoopSignalPayload(raw any) (messages.LoopSignalPayload, error) {
	switch got := raw.(type) {
	case nil:
		return messages.LoopSignalPayload{}, nil
	case messages.LoopSignalPayload:
		return got, nil
	case *messages.LoopSignalPayload:
		if got == nil {
			return messages.LoopSignalPayload{}, nil
		}
		return *got, nil
	case map[string]any:
		var payload messages.LoopSignalPayload
		if msg, ok := got["message"].(string); ok {
			payload.Message = msg
		}
		if force, ok := got["force_supervisor"].(bool); ok {
			payload.ForceSupervisor = force
		}
		return payload, nil
	default:
		return messages.LoopSignalPayload{}, fmt.Errorf("unsupported loop signal payload %T", raw)
	}
}

func summarizeSignalEnvelopes(envs []messages.Envelope) string {
	if len(envs) == 0 {
		return ""
	}
	type signalView struct {
		ID       string            `json:"id"`
		From     messages.Identity `json:"from"`
		Priority messages.Priority `json:"priority,omitempty"`
		Scope    []string          `json:"scope,omitempty"`
		Payload  map[string]any    `json:"payload,omitempty"`
	}
	views := make([]signalView, 0, len(envs))
	for _, env := range envs {
		payload, _ := decodeLoopSignalPayload(env.Payload)
		view := signalView{
			ID:       env.ID,
			From:     env.From,
			Priority: env.Priority,
			Scope:    append([]string(nil), env.Scope...),
		}
		if strings.TrimSpace(payload.Message) != "" || payload.ForceSupervisor {
			view.Payload = map[string]any{}
			if strings.TrimSpace(payload.Message) != "" {
				view.Payload["message"] = payload.Message
			}
			if payload.ForceSupervisor {
				view.Payload["force_supervisor"] = true
			}
		}
		views = append(views, view)
	}
	blob, err := json.Marshal(views)
	if err != nil {
		return ""
	}
	return "Signal envelopes for this run:\n" + string(blob)
}

func (l *Loop) enqueueSignal(env messages.Envelope) (SignalReceipt, error) {
	payload, err := decodeLoopSignalPayload(env.Payload)
	if err != nil {
		return SignalReceipt{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || !l.started {
		return SignalReceipt{}, fmt.Errorf("loop %q is not running", l.config.Name)
	}
	if l.config.WaitFunc != nil {
		return SignalReceipt{}, fmt.Errorf("loop %q is event-driven and cannot be interrupted by signal yet", l.config.Name)
	}

	l.pendingSignals = append(l.pendingSignals, pendingSignal{
		Envelope:        env,
		ForceSupervisor: payload.ForceSupervisor,
	})
	receipt := SignalReceipt{
		LoopID:          l.id,
		LoopName:        l.config.Name,
		State:           l.state,
		ForceSupervisor: payload.ForceSupervisor,
		PendingSignals:  len(l.pendingSignals),
	}
	if l.state == StateSleeping {
		select {
		case l.wakeCh <- struct{}{}:
		default:
		}
		receipt.WokeImmediately = true
	} else {
		receipt.QueuedForNextWake = true
	}
	return receipt, nil
}

func (l *Loop) consumePendingSignals() ([]messages.Envelope, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pendingSignals) == 0 {
		select {
		case <-l.wakeCh:
		default:
		}
		return nil, false
	}
	envs := make([]messages.Envelope, 0, len(l.pendingSignals))
	forceSupervisor := false
	for _, sig := range l.pendingSignals {
		envs = append(envs, sig.Envelope)
		forceSupervisor = forceSupervisor || sig.ForceSupervisor
	}
	l.pendingSignals = nil
	select {
	case <-l.wakeCh:
	default:
	}
	return envs, forceSupervisor
}

func (l *Loop) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-l.wakeCh:
		return true
	}
}
