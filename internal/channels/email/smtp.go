package email

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// sendMail connects to the account's SMTP server, authenticates, and
// delivers msg to recipients. Connections are per-send. The caller's
// context bounds the whole exchange: its deadline (or defaultOpTimeout)
// becomes the connection deadline, so a server that stalls after the
// greeting cannot hold the caller. Failures are [*DeliveryError]
// values classified by SMTP reply code.
//
// It is unexported on purpose: every path to SMTP goes through the
// package's send pipeline, so no caller can deliver mail around the
// checks that pipeline applies.
func sendMail(ctx context.Context, account string, cfg SMTPConfig, from string, recipients []string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	fail := func(op string, err error) error {
		return &DeliveryError{Account: account, Op: op, Kind: classifySMTPError(err), Err: err}
	}
	connectFail := func(op string, err error) error {
		return &DeliveryError{Account: account, Op: op, Kind: DeliveryConnect, Err: err}
	}
	tlsFail := func(op string, err error) error {
		return &DeliveryError{Account: account, Op: op, Kind: DeliveryTLS, Err: err}
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return connectFail("dial SMTP "+addr, err)
	}
	// One deadline for the whole exchange, derived from the caller.
	deadline := time.Now().Add(defaultOpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	var client *smtp.Client
	if !cfg.StartTLS {
		// Implicit TLS (port 465): the connection is TLS from the start.
		tlsConn := tls.Client(conn, tlsConfigFor(cfg.Host))
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return tlsFail("TLS handshake with "+addr, err)
		}
		client, err = smtp.NewClient(tlsConn, cfg.Host)
		if err != nil {
			_ = tlsConn.Close()
			return connectFail("read SMTP greeting from "+addr, err)
		}
	} else {
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return connectFail("read SMTP greeting from "+addr, err)
		}
	}
	defer client.Close()

	var fromDomain string
	if sender, err := parseAddress(from); err == nil {
		fromDomain = sender.Domain()
	}
	if err := client.Hello(heloName(fromDomain)); err != nil {
		return fail("EHLO", err)
	}

	if cfg.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return tlsFail("STARTTLS", errors.New("server does not offer STARTTLS; refusing to send credentials in the clear"))
		}
		if err := client.StartTLS(tlsConfigFor(cfg.Host)); err != nil {
			return tlsFail("STARTTLS", err)
		}
	}

	if cfg.Username != "" && cfg.Password != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			dErr := fail("AUTH", err)
			// net/smtp reports a rejected credential as a 535; treat any
			// failure at this step as authentication so the text says
			// the operator must act.
			if de, ok := dErr.(*DeliveryError); ok && de.Kind == DeliveryUnclassified {
				de.Kind = DeliveryAuthentication
			}
			return dErr
		}
	}

	if err := client.Mail(from); err != nil {
		return fail("MAIL FROM "+from, err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fail("RCPT TO "+rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fail("DATA", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fail("write message", err)
	}
	if err := w.Close(); err != nil {
		return fail("finish DATA", err)
	}
	if err := client.Quit(); err != nil {
		// The message was accepted at end-of-DATA; a failed QUIT does
		// not undo delivery and must not be reported as one.
		return nil
	}
	return nil
}

// heloName is the name announced in EHLO, which the submission server
// stamps into the Received header every recipient can read. It is the
// sender's domain, already public in From and Message-ID, rather than
// the machine's host name, which would leak the workstation's identity
// to every correspondent. With no domain to announce it falls back to
// the RFC 5321 address-literal form for the loopback address rather
// than "localhost", which is what a misconfigured bot says.
func heloName(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" || strings.ContainsAny(domain, " \t\r\n") || strings.EqualFold(domain, "localhost") {
		return "[127.0.0.1]"
	}
	return domain
}
