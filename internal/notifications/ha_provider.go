package notifications

import "context"

// HAPushProvider delivers notifications via Home Assistant companion
// app push by wrapping the existing [Sender].
type HAPushProvider struct {
	sender *Sender
}

// NewHAPushProvider creates a provider that delegates to an existing
// HA notification [Sender].
func NewHAPushProvider(sender *Sender) *HAPushProvider {
	return &HAPushProvider{sender: sender}
}

// Name returns the provider identifier.
func (p *HAPushProvider) Name() string { return "ha_push" }

// Send delivers a fire-and-forget notification via HA push.
func (p *HAPushProvider) Send(ctx context.Context, req NotificationRequest) error {
	return p.sender.Send(ctx, Notification{
		Recipient: req.Recipient,
		Title:     req.Title,
		Message:   req.Message,
		Priority:  req.Priority,
	})
}

// SendActionable delivers an actionable notification via HA push. The
// provider only handles delivery; record creation is the router's
// responsibility.
func (p *HAPushProvider) SendActionable(ctx context.Context, req ActionableRequest) error {
	return p.sender.Send(ctx, Notification{
		Recipient:     req.Recipient,
		Title:         req.Title,
		Message:       req.Message,
		Priority:      req.Priority,
		Actions:       req.Actions,
		RequestID:     req.RequestID,
		Timeout:       req.Timeout,
		TimeoutAction: req.TimeoutAction,
		Context:       req.Context,
	})
}
