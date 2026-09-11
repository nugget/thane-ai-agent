package email

import (
	"context"
	"errors"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSendMailOverSTARTTLS(t *testing.T) {
	fake := newSMTPFake(t, nil)
	cfg := fake.config("alice@example.com", "secret")
	msg := []byte("Subject: hello\r\n\r\nbody\r\n")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"bob@example.com", "carol@example.com"}, msg)
	if err != nil {
		t.Fatalf("sendMail: %v", err)
	}
	got := fake.received()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(got))
	}
	d := got[0]
	if !d.IsTLS {
		t.Error("message must have been delivered after STARTTLS")
	}
	if d.Auth == "" {
		t.Error("credentials configured, so AUTH PLAIN must have happened")
	}
	if d.From != "alice@example.com" {
		t.Errorf("MAIL FROM = %q", d.From)
	}
	if len(d.To) != 2 {
		t.Errorf("RCPT TO = %v", d.To)
	}
	if !strings.Contains(d.Data, "Subject: hello") {
		t.Errorf("DATA = %q", d.Data)
	}
	if d.Helo != "example.com" {
		t.Errorf("EHLO name = %q; must announce the sender's domain, not the machine", d.Helo)
	}
}

func TestSendMailOverImplicitTLS(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.implicit = true; f.starttls = false })
	cfg := fake.config("alice@example.com", "secret")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"bob@example.com"}, []byte("Subject: x\r\n\r\ny\r\n"))
	if err != nil {
		t.Fatalf("sendMail: %v", err)
	}
	if got := fake.received(); len(got) != 1 || !got[0].IsTLS {
		t.Fatalf("deliveries = %+v, want one TLS delivery", got)
	}
}

func TestSendMailRefusesPlaintextWhenSTARTTLSMissing(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.starttls = false })
	cfg := fake.config("alice@example.com", "secret")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"bob@example.com"}, []byte("x"))
	if DeliveryKindOf(err) != DeliveryTLS {
		t.Fatalf("err = %v (kind %q), want tls_failed refusal", err, DeliveryKindOf(err))
	}
	mustContain(t, err.Error(), "STARTTLS", `"primary"`, "operator", "no credential was sent")
	if len(fake.received()) != 0 {
		t.Error("nothing may be delivered in the clear")
	}
}

func TestSendMailClassifiesPermanentRecipientReject(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.rejectRcpt["nobody@example.com"] = 550 })
	cfg := fake.config("alice@example.com", "secret")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"nobody@example.com"}, []byte("x"))
	var dErr *DeliveryError
	if !errors.As(err, &dErr) {
		t.Fatalf("err = %v (%T), want *DeliveryError", err, err)
	}
	if dErr.Kind != DeliveryPermanent || dErr.Account != "primary" || !strings.HasPrefix(dErr.Op, "RCPT TO nobody@example.com") {
		t.Errorf("DeliveryError = %+v", dErr)
	}
	mustContain(t, err.Error(), "permanently refused", "recipient address")
}

func TestSendMailClassifiesTransientReject(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.rejectRcpt["later@example.com"] = 450 })
	cfg := fake.config("alice@example.com", "secret")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"later@example.com"}, []byte("x"))
	if DeliveryKindOf(err) != DeliveryTransient {
		t.Fatalf("err = %v (kind %q), want transient_reject", err, DeliveryKindOf(err))
	}
	mustContain(t, err.Error(), "temporarily refused", "retry later")
}

func TestSendMailClassifiesAuthFailure(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.rejectAuth = true })
	cfg := fake.config("alice@example.com", "wrong")

	err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"bob@example.com"}, []byte("x"))
	if DeliveryKindOf(err) != DeliveryAuthentication {
		t.Fatalf("err = %v (kind %q), want authentication_failed", err, DeliveryKindOf(err))
	}
	mustContain(t, err.Error(), "rejected the login", "operator")
}

func TestSendMailHonorsContextDeadline(t *testing.T) {
	host, port := hungListener(t)
	startTLS := true
	cfg := SMTPConfig{Host: host, Port: port, Username: "a", Password: "b", StartTLS: &startTLS}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := sendMail(ctx, "primary", cfg, "a@example.com", []string{"b@example.com"}, []byte("x"))
	if err == nil {
		t.Fatal("a server that never greets must fail")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("sendMail took %v; the deadline must bound the greeting wait", time.Since(start))
	}
	if DeliveryKindOf(err) != DeliveryConnect {
		t.Errorf("kind = %q, want connect_failed (%v)", DeliveryKindOf(err), err)
	}
}

func TestClassifySMTPError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want DeliveryKind
	}{
		{"nil", nil, DeliveryUnclassified},
		{"535 auth", &textproto.Error{Code: 535, Msg: "bad creds"}, DeliveryAuthentication},
		{"530 auth required", &textproto.Error{Code: 530, Msg: "auth required"}, DeliveryAuthentication},
		{"550 permanent", &textproto.Error{Code: 550, Msg: "no such user"}, DeliveryPermanent},
		{"552 too big", &textproto.Error{Code: 552, Msg: "too big"}, DeliveryPermanent},
		{"421 transient", &textproto.Error{Code: 421, Msg: "try later"}, DeliveryTransient},
		{"450 transient", &textproto.Error{Code: 450, Msg: "greylisted"}, DeliveryTransient},
		{"plain error", errors.New("boom"), DeliveryUnclassified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySMTPError(tc.err); got != tc.want {
				t.Errorf("classifySMTPError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeloNameAnnouncesTheSenderDomain(t *testing.T) {
	for in, want := range map[string]string{
		"example.com": "example.com",
		"":            "[127.0.0.1]",
		"bad host":    "[127.0.0.1]",
		"localhost":   "[127.0.0.1]",
	} {
		if got := heloName(in); got != want {
			t.Errorf("heloName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSendMailRejectsUntrustedCertificate is the negative control for
// TLS verification: a server whose chain the client does not trust is
// refused on both transports, classified as a TLS failure the
// operator must fix, and nothing is delivered.
func TestSendMailRejectsUntrustedCertificate(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		fake := newSMTPFake(t, func(f *smtpFake) {
			f.tlsCfg = untrustedServerTLS(t)
			f.implicit = implicit
		})
		cfg := fake.config("alice@example.com", "secret")
		err := sendMail(context.Background(), "primary", cfg, "alice@example.com", []string{"bob@example.com"}, []byte("x"))
		if DeliveryKindOf(err) != DeliveryTLS {
			t.Errorf("implicit=%v: err = %v (kind %q), want tls_failed", implicit, err, DeliveryKindOf(err))
		}
		if len(fake.received()) != 0 {
			t.Errorf("implicit=%v: nothing may be delivered over an untrusted connection", implicit)
		}
	}
}
