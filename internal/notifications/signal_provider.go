package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// SignalSender abstracts Signal message delivery so the notifications
// package does not import the signal package. Implemented by
// signal.Client.
type SignalSender interface {
	Send(ctx context.Context, recipient, message string) (int64, error)
}

// MessageRecorder records outbound messages in conversation memory so
// they appear in history when the recipient replies. Callers provide
// the conversation ID derivation appropriate for their channel.
type MessageRecorder interface {
	RecordOutbound(phone, message string) error
}

// SignalProvider delivers fire-and-forget notifications via Signal
// by resolving the recipient's phone number from the contact store.
// When a MessageRecorder is set, outbound notifications are recorded
// in conversation memory with provenance metadata.
type SignalProvider struct {
	sender   SignalSender
	contacts ContactResolver
	recorder MessageRecorder // optional; nil skips memory recording
	logger   *slog.Logger
}

// NewSignalProvider creates a Signal notification provider.
func NewSignalProvider(sender SignalSender, contacts ContactResolver, logger *slog.Logger) *SignalProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &SignalProvider{
		sender:   sender,
		contacts: contacts,
		logger:   logger,
	}
}

// SetRecorder configures memory recording for outbound notifications.
// When set, sent messages are recorded in conversation history with
// provenance metadata so the agent has context when the user replies.
func (p *SignalProvider) SetRecorder(r MessageRecorder) {
	p.recorder = r
}

// Name returns the provider identifier.
func (p *SignalProvider) Name() string { return "signal" }

// Send delivers a fire-and-forget notification via Signal. The
// recipient is resolved to a phone number via contact properties
// (IMPP with signal: prefix, or TEL).
func (p *SignalProvider) Send(ctx context.Context, req NotificationRequest) error {
	if p.sender == nil {
		return fmt.Errorf("signal provider: sender is nil")
	}
	if p.contacts == nil {
		return fmt.Errorf("signal provider: contact resolver is nil")
	}
	phone, err := p.resolvePhone(req.Recipient)
	if err != nil {
		return err
	}

	// Format: include title if present, otherwise just the message.
	msg := req.Message
	if req.Title != "" {
		msg = req.Title + "\n\n" + req.Message
	}

	if _, err := p.sender.Send(ctx, phone, msg); err != nil {
		return fmt.Errorf("signal send to %s: %w", req.Recipient, err)
	}

	// Record in conversation memory so the agent has context when the
	// user replies. The message is annotated with provenance metadata.
	if p.recorder != nil {
		annotated := fmt.Sprintf("[notification via signal | source: %s]\n%s",
			req.Priority, msg)
		if req.Priority == "" {
			annotated = fmt.Sprintf("[notification via signal]\n%s", msg)
		}
		if err := p.recorder.RecordOutbound(phone, annotated); err != nil {
			p.logger.Warn("failed to record notification in memory",
				"phone", phone, "error", err)
		}
	}

	p.logger.Info("signal notification sent",
		"recipient", req.Recipient,
		"phone", phone,
	)
	return nil
}

// SendActionable returns an error because Signal does not support
// interactive action buttons. The router should fall back to a
// provider that supports structured actions (e.g., ha_push).
func (p *SignalProvider) SendActionable(_ context.Context, req ActionableRequest) error {
	return fmt.Errorf("signal provider does not support actionable notifications (recipient: %s); use a provider with button support", req.Recipient)
}

// resolvePhone looks up the recipient's phone number from contact
// properties. Checks IMPP (signal:+phone) first, then TEL.
func (p *SignalProvider) resolvePhone(recipient string) (string, error) {
	contact, err := p.contacts.ResolveContact(recipient)
	if err != nil {
		return "", fmt.Errorf("resolve signal recipient %q: %w", recipient, err)
	}

	props, err := p.contacts.GetPropertiesMap(contact.ID)
	if err != nil {
		return "", fmt.Errorf("lookup properties for %q: %w", recipient, err)
	}

	// Prefer IMPP with signal: prefix (explicit Signal identity).
	if impp, ok := props["IMPP"]; ok {
		for _, v := range impp {
			if strings.HasPrefix(v, "signal:") {
				phone := strings.TrimSpace(strings.TrimPrefix(v, "signal:"))
				if phone != "" {
					return phone, nil
				}
			}
		}
	}

	// Fall back to TEL property.
	if tel, ok := props["TEL"]; ok && len(tel) > 0 {
		phone := strings.TrimSpace(tel[0])
		if phone != "" {
			return phone, nil
		}
	}

	return "", fmt.Errorf("no phone number found for contact %q", recipient)
}
