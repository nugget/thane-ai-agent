package email

import platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"

// Config is the serialized email configuration contract. The struct
// lives in the config package so the generated example config carries
// its field documentation; this alias keeps the email package's
// vocabulary.
type Config = platformconfig.EmailConfig

// AccountConfig is the serialized configuration for one mailbox.
type AccountConfig = platformconfig.EmailAccountConfig

// IMAPConfig holds IMAP server connection parameters.
type IMAPConfig = platformconfig.EmailIMAPConfig

// SMTPConfig holds SMTP server connection parameters for sending.
type SMTPConfig = platformconfig.EmailSMTPConfig

// PolicyConfig is one account's access level and delivery policy.
type PolicyConfig = platformconfig.EmailPolicyConfig

// Access levels, in the email package's vocabulary.
const (
	AccessRead     = platformconfig.EmailAccessRead
	AccessOrganize = platformconfig.EmailAccessOrganize
	AccessSend     = platformconfig.EmailAccessSend
)

// Delivery modes, in the email package's vocabulary.
const (
	DeliveryByTrustZone = platformconfig.EmailDeliveryByTrustZone
	DeliveryDrafts      = platformconfig.EmailDeliveryDrafts
	DeliveryDirect      = platformconfig.EmailDeliveryDirect
)
