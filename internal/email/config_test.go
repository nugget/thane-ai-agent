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
