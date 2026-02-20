package email

import "testing"

func TestConfig_Configured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "no accounts",
			cfg:  Config{},
			want: false,
		},
		{
			name: "empty account",
			cfg:  Config{Accounts: []AccountConfig{{Name: "test"}}},
			want: false,
		},
		{
			name: "host only",
			cfg:  Config{Accounts: []AccountConfig{{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com"}}}},
			want: false,
		},
		{
			name: "host and username",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.example.com", Username: "user@example.com"},
			}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com", Username: "user"}},
		},
	}
	cfg.ApplyDefaults()

	if cfg.Accounts[0].IMAP.Port != 993 {
		t.Errorf("default port = %d, want 993", cfg.Accounts[0].IMAP.Port)
	}
	if !cfg.Accounts[0].IMAP.TLS {
		t.Error("default TLS should be true")
	}
}

func TestConfig_ApplyDefaults_Port143(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com", Username: "user", Port: 143}},
		},
	}
	cfg.ApplyDefaults()

	if cfg.Accounts[0].IMAP.TLS {
		t.Error("TLS should remain false for port 143")
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "personal",
				IMAP: IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user@gmail.com"},
			}}},
			wantErr: false,
		},
		{
			name: "missing name",
			cfg: Config{Accounts: []AccountConfig{{
				IMAP: IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
			}}},
			wantErr: true,
		},
		{
			name: "duplicate names",
			cfg: Config{Accounts: []AccountConfig{
				{Name: "work", IMAP: IMAPConfig{Host: "imap.work.com", Port: 993, Username: "u1"}},
				{Name: "work", IMAP: IMAPConfig{Host: "imap.other.com", Port: 993, Username: "u2"}},
			}},
			wantErr: true,
		},
		{
			name: "missing host",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "test",
				IMAP: IMAPConfig{Port: 993, Username: "user"},
			}}},
			wantErr: true,
		},
		{
			name: "missing username",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.gmail.com", Port: 993},
			}}},
			wantErr: true,
		},
		{
			name: "invalid port",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.gmail.com", Port: 0, Username: "user"},
			}}},
			wantErr: true,
		},
		{
			name: "valid with SMTP",
			cfg: Config{Accounts: []AccountConfig{{
				Name:        "test",
				IMAP:        IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.gmail.com", Port: 587, Username: "user", Password: "pass"},
				DefaultFrom: "User <user@gmail.com>",
			}}},
			wantErr: false,
		},
		{
			name: "smtp missing password",
			cfg: Config{Accounts: []AccountConfig{{
				Name:        "test",
				IMAP:        IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.gmail.com", Port: 587, Username: "user"},
				DefaultFrom: "User <user@gmail.com>",
			}}},
			wantErr: true,
		},
		{
			name: "smtp missing username",
			cfg: Config{Accounts: []AccountConfig{{
				Name:        "test",
				IMAP:        IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.gmail.com", Port: 587},
				DefaultFrom: "User <user@gmail.com>",
			}}},
			wantErr: true,
		},
		{
			name: "smtp missing default_from",
			cfg: Config{Accounts: []AccountConfig{{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
				SMTP: SMTPConfig{Host: "smtp.gmail.com", Port: 587, Username: "user", Password: "pass"},
			}}},
			wantErr: true,
		},
		{
			name: "smtp invalid port",
			cfg: Config{Accounts: []AccountConfig{{
				Name:        "test",
				IMAP:        IMAPConfig{Host: "imap.gmail.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.gmail.com", Port: 0, Username: "user", Password: "pass"},
				DefaultFrom: "User <user@gmail.com>",
			}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_ApplyDefaults_SMTP(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.example.com", Username: "user"},
				SMTP: SMTPConfig{Host: "smtp.example.com", Username: "user"},
			},
		},
	}
	cfg.ApplyDefaults()

	if cfg.Accounts[0].SMTP.Port != 587 {
		t.Errorf("default SMTP port = %d, want 587", cfg.Accounts[0].SMTP.Port)
	}
	if !cfg.Accounts[0].SMTP.StartTLS {
		t.Error("default SMTP StartTLS should be true")
	}
}

func TestConfig_ApplyDefaults_SMTP_Port465(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name: "test",
				IMAP: IMAPConfig{Host: "imap.example.com", Username: "user"},
				SMTP: SMTPConfig{Host: "smtp.example.com", Username: "user", Port: 465},
			},
		},
	}
	cfg.ApplyDefaults()

	if cfg.Accounts[0].SMTP.StartTLS {
		t.Error("SMTP StartTLS should remain false for port 465 (implicit TLS)")
	}
}

func TestConfig_ApplyDefaults_NoSMTP(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com", Username: "user"}},
		},
	}
	cfg.ApplyDefaults()

	// SMTP defaults should not be applied when host is empty.
	if cfg.Accounts[0].SMTP.Port != 0 {
		t.Errorf("SMTP port should remain 0 when host is empty, got %d", cfg.Accounts[0].SMTP.Port)
	}
}

func TestAccountConfig_SMTPConfigured(t *testing.T) {
	tests := []struct {
		name string
		acct AccountConfig
		want bool
	}{
		{"no smtp", AccountConfig{}, false},
		{"host only", AccountConfig{SMTP: SMTPConfig{Host: "smtp.example.com"}}, false},
		{"host and username", AccountConfig{SMTP: SMTPConfig{Host: "smtp.example.com", Username: "user"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.acct.SMTPConfigured(); got != tt.want {
				t.Errorf("SMTPConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccountConfig_SentFolder(t *testing.T) {
	acct := AccountConfig{
		Name:       "test",
		SentFolder: "[Gmail]/Sent Mail",
	}
	if acct.SentFolder != "[Gmail]/Sent Mail" {
		t.Errorf("SentFolder = %q, want %q", acct.SentFolder, "[Gmail]/Sent Mail")
	}

	// Empty SentFolder means no IMAP APPEND after send.
	empty := AccountConfig{Name: "test"}
	if empty.SentFolder != "" {
		t.Errorf("SentFolder should default to empty, got %q", empty.SentFolder)
	}
}

func TestConfig_BccOwner(t *testing.T) {
	cfg := Config{
		BccOwner: "owner@example.com",
		Accounts: []AccountConfig{
			{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com", Username: "user"}},
		},
	}

	if cfg.BccOwner != "owner@example.com" {
		t.Errorf("BccOwner = %q, want %q", cfg.BccOwner, "owner@example.com")
	}
	if !cfg.Configured() {
		t.Error("Configured() should be true when BccOwner is set with valid account")
	}
}
