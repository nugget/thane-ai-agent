package config

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// defaultEmailPollIntervalSec is the poll interval applied when email is
// configured and the operator did not set one.
const defaultEmailPollIntervalSec = 300

// Email access levels: the most the model may do with an account.
const (
	// EmailAccessRead permits listing, searching, and reading. Reading
	// does not mark messages seen at this level.
	EmailAccessRead = "read"

	// EmailAccessOrganize also permits changing flags and moving mail.
	EmailAccessOrganize = "organize"

	// EmailAccessSend also permits composing, replying, and drafting.
	EmailAccessSend = "send"
)

// Email delivery modes: where outbound mail goes once every recipient
// has passed the trust gate.
const (
	// EmailDeliveryByTrustZone sends directly to admin and household
	// recipients when a human is attending the turn, holds mail for
	// trusted recipients in Drafts, refuses known and unknown ones,
	// and holds everything an unattended loop writes.
	EmailDeliveryByTrustZone = "by_trust_zone"

	// EmailDeliveryDrafts holds every message in Drafts.
	EmailDeliveryDrafts = "drafts"

	// EmailDeliveryDirect sends everything the gate allows.
	EmailDeliveryDirect = "direct"
)

// EmailConfig configures native IMAP and SMTP email. When at least one
// account has an IMAP host and username, Thane lists, reads, searches,
// files, and (for accounts with smtp configured) sends mail directly,
// and polls each account's INBOX for new messages that wake the
// email-default-handler loop.
type EmailConfig struct {
	// BccOwner is an address that receives a blind copy of every message
	// the agent sends, unless it is already a recipient. It is an audit
	// copy for the operator, not a recipient the agent chose, and it
	// does not need a contact record. Leave empty to disable.
	BccOwner string `yaml:"bcc_owner"`

	// PollInterval is how often, in seconds, every account's INBOX is
	// checked for new mail. Default: 300 when at least one account is
	// configured. Set to 0 to disable polling; the built-in email-poller
	// and email-default-handler loops are then not created, and mail is
	// only ever read when a tool asks for it. Negative values are
	// rejected.
	PollInterval *int `yaml:"poll_interval"`

	// Accounts lists the mailboxes Thane connects to. The first account is
	// the primary: tools use it when no account is named and no loop
	// binding selects one.
	Accounts []EmailAccountConfig `yaml:"accounts"`
}

// EmailAccountConfig describes one mailbox: its IMAP connection, an
// optional SMTP connection for sending, and the folders Thane writes
// copies into.
type EmailAccountConfig struct {
	// Name is the short identifier tools and loop bindings use for this
	// account (for example "personal" or "packages"). Required and unique.
	Name string `yaml:"name"`

	// Description is an operator-authored note about what this mailbox is
	// for, shown to the model beside the account name so it learns the
	// account's purpose and limits before acting rather than by being
	// refused. For example: "parcel and delivery notifications; never
	// sends".
	Description string `yaml:"description"`

	// IMAP configures the connection used to read and organize mail.
	IMAP EmailIMAPConfig `yaml:"imap"`

	// SMTP configures the connection used to send mail. Omit to make the
	// account read-and-organize only.
	SMTP EmailSMTPConfig `yaml:"smtp"`

	// DefaultFrom is the From address for mail sent from this account,
	// for example "Thane <thane@example.com>". Required when smtp is
	// configured. It is also how the poller recognises the account's own
	// outbound copies in INBOX and skips them.
	DefaultFrom string `yaml:"default_from"`

	// SentFolder is the IMAP folder that receives a copy of every message
	// sent from this account, for example "Sent" or "[Gmail]/Sent Mail".
	// Leave empty to keep no server-side copy.
	SentFolder string `yaml:"sent_folder"`

	// DraftsFolder is the IMAP folder that receives messages held for the
	// operator to send, for example "Drafts" or "[Gmail]/Drafts". Leave
	// empty to use the folder the server marks as its drafts folder, or
	// "Drafts" when it marks none.
	DraftsFolder string `yaml:"drafts_folder"`

	// Policy is what the model may do with this account beyond reading
	// it, and where the mail it writes goes.
	Policy EmailPolicyConfig `yaml:"policy"`
}

// EmailPolicyConfig is one account's access level and delivery policy.
// Every outbound message ends in exactly one disposition: sent, held
// in Drafts for the operator, or refused with a record of why.
type EmailPolicyConfig struct {
	// Access is the most the model may do with this account: "read"
	// (list, search, and read without marking messages seen),
	// "organize" (also flag and move), or "send" (also compose, reply,
	// and draft). Default: "send" when smtp is configured, "organize"
	// otherwise. An account with smtp configured may still be held at
	// "organize", which keeps its credentials for the operator's own
	// use; "send" without smtp can only draft and requires
	// delivery: drafts.
	Access string `yaml:"access"`

	// Delivery decides where outbound mail goes once every recipient
	// has passed the trust gate. "by_trust_zone" (default) sends
	// directly to admin and household recipients when a human is
	// attending the turn, holds mail for trusted recipients in the
	// Drafts folder for the operator to send, and refuses known and
	// unknown recipients; a turn no human is attending (a poller wake
	// or a scheduled loop rather than a conversation with the operator)
	// holds everything in Drafts, so an autonomous loop never sends on
	// its own. "drafts" holds every message in Drafts. "direct" sends
	// everything the gate allows, including to trusted recipients and
	// from unattended loops; choose it deliberately.
	Delivery string `yaml:"delivery"`

	// DeniedRecipientDomains lists domains this account never writes
	// to, even when the recipient is a trusted contact. An entry covers
	// the domain and its subdomains ("example.com" also covers
	// mail.example.com). Checked before allowed_recipient_domains.
	DeniedRecipientDomains []string `yaml:"denied_recipient_domains"`

	// AllowedRecipientDomains, when set, limits outbound recipients to
	// these domains and their subdomains; every other recipient is
	// refused. Empty means no limit.
	AllowedRecipientDomains []string `yaml:"allowed_recipient_domains"`
}

// EmailIMAPConfig holds IMAP server connection parameters.
type EmailIMAPConfig struct {
	// Host is the IMAP server hostname, for example "imap.example.com".
	Host string `yaml:"host"`

	// Port is the IMAP server port. Default: 993.
	Port int `yaml:"port"`

	// Username is the IMAP login, typically the mailbox address.
	Username string `yaml:"username"`

	// Password is the IMAP login password. Environment variable
	// expansion such as ${IMAP_PASSWORD} is available.
	Password string `yaml:"password"`

	// TLS controls whether the connection is TLS from the first byte.
	// Default: true on every port except 143. When false, the connection
	// starts in plaintext and is upgraded with STARTTLS before the login;
	// a server that does not offer STARTTLS is refused rather than sent
	// credentials in the clear.
	TLS *bool `yaml:"tls"`
}

// TLSEnabled reports whether the IMAP connection uses TLS, applying the
// port-based default when the field was not set.
func (c EmailIMAPConfig) TLSEnabled() bool {
	if c.TLS != nil {
		return *c.TLS
	}
	return c.Port != 143
}

// EmailSMTPConfig holds SMTP server connection parameters for sending.
type EmailSMTPConfig struct {
	// Host is the SMTP submission server hostname, for example
	// "smtp.example.com". Setting it enables sending from the account.
	Host string `yaml:"host"`

	// Port is the SMTP server port. Default: 587.
	Port int `yaml:"port"`

	// Username is the SMTP login, typically the mailbox address.
	Username string `yaml:"username"`

	// Password is the SMTP login password. Environment variable
	// expansion such as ${SMTP_PASSWORD} is available.
	Password string `yaml:"password"`

	// StartTLS controls whether the connection starts in plaintext and is
	// upgraded with STARTTLS (submission on 587) rather than being TLS
	// from the first byte (465). Default: true on every port except 465.
	// A server that does not offer STARTTLS is refused rather than sent
	// credentials in the clear.
	StartTLS *bool `yaml:"starttls"`
}

// StartTLSEnabled reports whether the SMTP connection upgrades with
// STARTTLS, applying the port-based default when the field was not set.
func (c EmailSMTPConfig) StartTLSEnabled() bool {
	if c.StartTLS != nil {
		return *c.StartTLS
	}
	return c.Port != 465
}

// Configured reports whether at least one account has the minimum IMAP
// configuration (host and username).
func (c EmailConfig) Configured() bool {
	for _, a := range c.Accounts {
		if a.IMAP.Host != "" && a.IMAP.Username != "" {
			return true
		}
	}
	return false
}

// PollingInterval returns how often INBOX is checked, or zero when
// polling is disabled or email is not configured.
func (c EmailConfig) PollingInterval() time.Duration {
	if !c.Configured() {
		return 0
	}
	seconds := defaultEmailPollIntervalSec
	if c.PollInterval != nil {
		seconds = *c.PollInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// SMTPConfigured reports whether this account has an SMTP connection.
// Whether the model may use it is [EmailAccountConfig.CanDeliver].
func (a EmailAccountConfig) SMTPConfigured() bool {
	return a.SMTP.Host != "" && a.SMTP.Username != ""
}

// AccessLevel returns the effective access level, applying the
// documented default when the policy does not set one.
func (a EmailAccountConfig) AccessLevel() string {
	if a.Policy.Access != "" {
		return a.Policy.Access
	}
	if a.SMTPConfigured() {
		return EmailAccessSend
	}
	return EmailAccessOrganize
}

// DeliveryMode returns the effective delivery mode.
func (a EmailAccountConfig) DeliveryMode() string {
	if a.Policy.Delivery != "" {
		return a.Policy.Delivery
	}
	return EmailDeliveryByTrustZone
}

// CanDraft reports whether the model may write outbound mail from this
// account at all, to SMTP or to Drafts.
func (a EmailAccountConfig) CanDraft() bool {
	return a.AccessLevel() == EmailAccessSend
}

// CanDeliver reports whether the model may hand mail from this account
// to SMTP itself: access is send and smtp is configured.
func (a EmailAccountConfig) CanDeliver() bool {
	return a.CanDraft() && a.SMTPConfigured()
}

// validEmailAccess is the set of access levels an operator may write.
var validEmailAccess = map[string]bool{EmailAccessRead: true, EmailAccessOrganize: true, EmailAccessSend: true}

// validEmailDelivery is the set of delivery modes an operator may write.
var validEmailDelivery = map[string]bool{EmailDeliveryByTrustZone: true, EmailDeliveryDrafts: true, EmailDeliveryDirect: true}

// normalizeDomains lowercases, trims, and strips a leading dot from
// each domain, dropping empties.
func normalizeDomains(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// ApplyDefaults fills unset fields with their documented defaults.
// Called by the parent config's applyDefaults method.
func (c *EmailConfig) ApplyDefaults() {
	if c.PollInterval == nil && c.Configured() {
		v := defaultEmailPollIntervalSec
		c.PollInterval = &v
	}

	for i := range c.Accounts {
		acct := &c.Accounts[i]
		if acct.IMAP.Port == 0 {
			acct.IMAP.Port = 993
		}
		if acct.IMAP.TLS == nil {
			v := acct.IMAP.TLSEnabled()
			acct.IMAP.TLS = &v
		}
		if acct.SMTP.Host != "" {
			if acct.SMTP.Port == 0 {
				acct.SMTP.Port = 587
			}
			if acct.SMTP.StartTLS == nil {
				v := acct.SMTP.StartTLSEnabled()
				acct.SMTP.StartTLS = &v
			}
		}
		acct.Policy.Access = acct.AccessLevel()
		acct.Policy.Delivery = acct.DeliveryMode()
		acct.Policy.DeniedRecipientDomains = normalizeDomains(acct.Policy.DeniedRecipientDomains)
		acct.Policy.AllowedRecipientDomains = normalizeDomains(acct.Policy.AllowedRecipientDomains)
	}
}

// Validate checks that the email configuration is internally
// consistent. Errors name the YAML path of the first problem found.
func (c EmailConfig) Validate() error {
	if c.PollInterval != nil && *c.PollInterval < 0 {
		return fmt.Errorf("email.poll_interval must be >= 0 (0 disables polling), got %d", *c.PollInterval)
	}
	if c.BccOwner != "" {
		if _, err := mail.ParseAddress(strings.TrimSpace(c.BccOwner)); err != nil {
			return fmt.Errorf("email.bcc_owner %q is not a valid email address: %w", c.BccOwner, err)
		}
	}

	names := make(map[string]bool, len(c.Accounts))
	for i, a := range c.Accounts {
		if a.Name == "" {
			return fmt.Errorf("email.accounts[%d].name must not be empty", i)
		}
		if names[a.Name] {
			return fmt.Errorf("email.accounts[%d].name %q is a duplicate", i, a.Name)
		}
		names[a.Name] = true

		if a.IMAP.Host == "" {
			return fmt.Errorf("email.accounts[%d] (%s): imap.host is required", i, a.Name)
		}
		if a.IMAP.Username == "" {
			return fmt.Errorf("email.accounts[%d] (%s): imap.username is required", i, a.Name)
		}
		if a.IMAP.Port < 1 || a.IMAP.Port > 65535 {
			return fmt.Errorf("email.accounts[%d] (%s): imap.port %d out of range (1-65535)", i, a.Name, a.IMAP.Port)
		}

		if a.SMTP.Host != "" {
			if a.SMTP.Username == "" {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.username is required when smtp.host is set", i, a.Name)
			}
			if a.SMTP.Password == "" {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.password is required when smtp.host is set", i, a.Name)
			}
			if a.SMTP.Port < 1 || a.SMTP.Port > 65535 {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.port %d out of range (1-65535)", i, a.Name, a.SMTP.Port)
			}
			if a.DefaultFrom == "" {
				return fmt.Errorf("email.accounts[%d] (%s): default_from is required when smtp is configured", i, a.Name)
			}
		}
		if a.DefaultFrom != "" {
			if _, err := mail.ParseAddress(strings.TrimSpace(a.DefaultFrom)); err != nil {
				return fmt.Errorf("email.accounts[%d] (%s): default_from %q is not a valid email address: %w", i, a.Name, a.DefaultFrom, err)
			}
		}
		if err := a.validatePolicy(i); err != nil {
			return err
		}
	}
	return nil
}

// validatePolicy checks the account's access and delivery policy.
func (a EmailAccountConfig) validatePolicy(i int) error {
	if a.Policy.Access != "" && !validEmailAccess[a.Policy.Access] {
		return fmt.Errorf("email.accounts[%d] (%s): policy.access %q is not one of read, organize, send", i, a.Name, a.Policy.Access)
	}
	if a.Policy.Delivery != "" && !validEmailDelivery[a.Policy.Delivery] {
		return fmt.Errorf("email.accounts[%d] (%s): policy.delivery %q is not one of by_trust_zone, drafts, direct", i, a.Name, a.Policy.Delivery)
	}
	if a.CanDraft() && !a.SMTPConfigured() {
		if a.DeliveryMode() != EmailDeliveryDrafts {
			return fmt.Errorf("email.accounts[%d] (%s): policy.access send without smtp can only draft; set policy.delivery: drafts or configure smtp", i, a.Name)
		}
		if a.DefaultFrom == "" {
			return fmt.Errorf("email.accounts[%d] (%s): default_from is required when policy.access is send", i, a.Name)
		}
	}
	for _, list := range []struct {
		key     string
		domains []string
	}{{"denied_recipient_domains", a.Policy.DeniedRecipientDomains}, {"allowed_recipient_domains", a.Policy.AllowedRecipientDomains}} {
		for _, d := range list.domains {
			d = strings.TrimSpace(d)
			if d == "" || strings.ContainsAny(d, "@ /") {
				return fmt.Errorf("email.accounts[%d] (%s): policy.%s entry %q is not a domain", i, a.Name, list.key, d)
			}
		}
	}
	return nil
}
