package loop

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TriggerRequest identifies a running loop to wake immediately and carries
// one-shot execution overrides for that triggered run.
type TriggerRequest struct {
	LoopID          string `json:"loop_id,omitempty"`
	Name            string `json:"name,omitempty"`
	ForceSupervisor bool   `json:"force_supervisor,omitempty"`
	ContextMessage  string `json:"context_message,omitempty"`
}

// TriggerResult reports the outcome of a successful loop wake trigger.
type TriggerResult struct {
	LoopID          string `json:"loop_id"`
	Name            string `json:"name"`
	StateBefore     State  `json:"state_before"`
	ForceSupervisor bool   `json:"force_supervisor"`
	ContextInjected bool   `json:"context_injected"`
}

// TriggerOptions contains one-shot overrides for a triggered run.
type TriggerOptions struct {
	ForceSupervisor bool
	ContextMessage  string
}

// TriggerRun wakes a sleeping timer-driven loop immediately and applies any
// one-shot trigger overrides to the next iteration.
func (l *Loop) TriggerRun(opts TriggerOptions) (TriggerResult, error) {
	if l == nil {
		return TriggerResult{}, fmt.Errorf("loop: nil loop")
	}

	msg := strings.TrimSpace(opts.ContextMessage)

	l.mu.Lock()
	state := l.state
	if l.config.WaitFunc != nil {
		l.mu.Unlock()
		return TriggerResult{}, fmt.Errorf("loop %q is event-driven and does not support trigger_run", l.config.Name)
	}
	if state != StateSleeping {
		l.mu.Unlock()
		return TriggerResult{}, fmt.Errorf("loop %q is %s, not sleeping", l.config.Name, state)
	}
	if opts.ForceSupervisor {
		l.forceNextSupervisor = true
	}
	if msg != "" {
		l.triggerContextMessages = append(l.triggerContextMessages, msg)
	}
	wakeCh := l.triggerWakeCh
	result := TriggerResult{
		LoopID:          l.id,
		Name:            l.config.Name,
		StateBefore:     state,
		ForceSupervisor: opts.ForceSupervisor,
		ContextInjected: msg != "",
	}
	l.mu.Unlock()

	select {
	case wakeCh <- struct{}{}:
	default:
	}

	return result, nil
}

func (l *Loop) consumeTriggerOverrides() (bool, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	forceSupervisor := l.forceNextSupervisor
	l.forceNextSupervisor = false

	messages := append([]string(nil), l.triggerContextMessages...)
	l.triggerContextMessages = nil

	return forceSupervisor, messages
}

func (r *Registry) TriggerRun(req TriggerRequest) (TriggerResult, error) {
	if r == nil {
		return TriggerResult{}, fmt.Errorf("loop: registry is nil")
	}

	var l *Loop
	if strings.TrimSpace(req.LoopID) != "" {
		l = r.Get(strings.TrimSpace(req.LoopID))
		if l == nil {
			return TriggerResult{}, fmt.Errorf("loop %q not found", strings.TrimSpace(req.LoopID))
		}
	} else {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return TriggerResult{}, fmt.Errorf("name or loop_id is required")
		}
		l = r.GetByName(name)
		if l == nil {
			return TriggerResult{}, fmt.Errorf("loop %q not found", name)
		}
	}

	return l.TriggerRun(TriggerOptions{
		ForceSupervisor: req.ForceSupervisor,
		ContextMessage:  req.ContextMessage,
	})
}

func sleepCtxOrWake(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-timer.C:
		return true
	}
}

func formatTriggerContext(messages []string) string {
	if len(messages) == 0 {
		return ""
	}
	return "Triggered context:\n" + strings.Join(messages, "\n\n") + "\n\n"
}
