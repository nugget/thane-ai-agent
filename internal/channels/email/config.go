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
