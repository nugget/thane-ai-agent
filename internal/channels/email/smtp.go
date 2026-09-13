package email

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"strconv"
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
		return &DeliveryError{Account: account, Op: op, Kind: deliveryKind(ctx, classifySMTPError(err)), Err: err}
	}
	connectFail := func(op string, err error) error {
		return &DeliveryError{Account: account, Op: op, Kind: deliveryKind(ctx, DeliveryConnect), Err: err}
	}
	tlsFail := func(op string, err error) error {
		return &DeliveryError{Account: account, Op: op, Kind: deliveryKind(ctx, DeliveryTLS), Err: err}
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return connectFail("dial SMTP "+addr, err)
	}
	// The deadline below bounds a stalled exchange; this watcher makes a
	// cancelled context end it too, instead of waiting out the deadline.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
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

	if err := client.Hello(heloName(conn.LocalAddr())); err != nil {
		return fail("EHLO", err)
	}

	if cfg.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return tlsFail("STARTTLS", errors.New("server does not offer STARTTLS; refusing to send credentials in the clear"))
		}
		if err := client.StartTLS(tlsConfigFor(cfg.Host)); err != nil {
			// A 454 is a temporary TLS outage the server expects to
			// clear; anything else means the connection cannot be made
			// secure.
			if classifySMTPError(err) == DeliveryTransient {
				return fail("STARTTLS", err)
			}
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

// heloName is the EHLO argument: the address literal of the
// connection's local end, which RFC 5321 accepts from a client without a
// suitable host name. It identifies this connection honestly without
// announcing the workstation's host name, which the submission server
// would stamp into the Received header every recipient reads.
func heloName(local net.Addr) string {
	if tcp, ok := local.(*net.TCPAddr); ok && tcp.IP != nil && !tcp.IP.IsUnspecified() {
		if ip4 := tcp.IP.To4(); ip4 != nil {
			return "[" + ip4.String() + "]"
		}
		return "[IPv6:" + tcp.IP.String() + "]"
	}
	return "[127.0.0.1]"
}

// deliveryKind reports a failure the caller caused by cancelling as
// cancelled, whatever the connection error looked like, since closing
// the connection is how cancellation ends an exchange. An expired
// deadline is not a cancellation: it keeps its own class, so a stalled
// server still reads as a connect or transient failure worth a retry.
func deliveryKind(ctx context.Context, kind DeliveryKind) DeliveryKind {
	if errors.Is(ctx.Err(), context.Canceled) {
		return DeliveryCancelled
	}
	return kind
}
