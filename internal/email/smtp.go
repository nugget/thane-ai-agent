package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

// smtpDialTimeout is the maximum time to establish an SMTP connection.
const smtpDialTimeout = 30 * time.Second

// SendMail connects to the SMTP server, authenticates, and delivers the
// given message. Connections are ephemeral — each call opens and closes
// its own connection. The msg parameter should be a complete RFC 5322
// message (as returned by ComposeMessage). The context controls the
// overall deadline for the entire send operation.
func SendMail(ctx context.Context, cfg SMTPConfig, from string, recipients []string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	// Use context deadline for the dial timeout, falling back to the
	// package default.
	dialTimeout := smtpDialTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < dialTimeout {
			dialTimeout = remaining
		}
	}

	dialer := &net.Dialer{Timeout: dialTimeout}

	var client *smtp.Client
	var err error

	if !cfg.StartTLS {
		// Implicit TLS (port 465): connect over TLS from the start.
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		conn, dialErr := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if dialErr != nil {
			return fmt.Errorf("dial SMTPS %s: %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("create SMTP client on %s: %w", addr, err)
		}
	} else {
		// STARTTLS (port 587): connect plain, then upgrade.
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("dial SMTP %s: %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("create SMTP client on %s: %w", addr, err)
		}
	}
	defer client.Close()

	// EHLO.
	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}

	// Upgrade to TLS if using STARTTLS.
	if cfg.StartTLS {
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}

	// Authenticate if credentials are provided.
	if cfg.Username != "" && cfg.Password != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("AUTH: %w", err)
		}
	}

	// Set the sender.
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}

	// Set all recipients (To + Cc + Bcc).
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}

	// Write the message body.
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close DATA: %w", err)
	}

	return client.Quit()
}

// extractAddress extracts the bare email address from a string that
// may be in "Name <addr>" or just "addr" format.
func extractAddress(s string) string {
	if idx := len(s) - 1; idx > 0 && s[idx] == '>' {
		if start := lastIndexByte(s, '<'); start >= 0 {
			return s[start+1 : idx]
		}
	}
	return s
}

// lastIndexByte returns the index of the last occurrence of c in s, or -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// collectRecipients gathers all unique bare email addresses from the
// To, Cc, and Bcc fields for SMTP RCPT TO commands.
func collectRecipients(to, cc, bcc []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, lists := range [][]string{to, cc, bcc} {
		for _, addr := range lists {
			bare := extractAddress(addr)
			if bare != "" && !seen[bare] {
				seen[bare] = true
				result = append(result, bare)
			}
		}
	}

	return result
}
