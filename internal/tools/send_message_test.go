package tools

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

func TestHandleSendMessage(t *testing.T) {
	// Fake HA server that accepts any service call.
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ha := homeassistant.NewClient(srv.URL, "test-token", slog.Default())

	tests := []struct {
		name         string
		service      string
		title        string
		ha           *homeassistant.Client
		args         map[string]any
		wantErr      string
		wantContains string
		wantHAPath   string
	}{
		{
			name:    "unconfigured service",
			service: "",
			ha:      ha,
			args:    map[string]any{"message": "hello"},
			wantErr: "not configured",
		},
		{
			name:    "no HA client",
			service: "notify/mobile_app_test",
			ha:      nil,
			args:    map[string]any{"message": "hello"},
			wantErr: "requires Home Assistant",
		},
		{
			name:    "missing message",
			service: "notify/mobile_app_test",
			ha:      ha,
			args:    map[string]any{},
			wantErr: "message is required",
		},
		{
			name:    "invalid service format",
			service: "bad-format",
			ha:      ha,
			args:    map[string]any{"message": "hello"},
			wantErr: "invalid notification service format",
		},
		{
			name:         "notify service success",
			service:      "notify/mobile_app_test",
			title:        "Thane",
			ha:           ha,
			args:         map[string]any{"message": "Garage door open"},
			wantContains: "Notification sent via notify/mobile_app_test",
			wantHAPath:   "/api/services/notify/mobile_app_test",
		},
		{
			name:         "custom title override",
			service:      "notify/mobile_app_test",
			title:        "Default Title",
			ha:           ha,
			args:         map[string]any{"message": "Alert!", "title": "Custom"},
			wantContains: "Notification sent",
			wantHAPath:   "/api/services/notify/mobile_app_test",
		},
		{
			name:         "persistent notification",
			service:      "persistent_notification/create",
			title:        "Thane",
			ha:           ha,
			args:         map[string]any{"message": "Reminder"},
			wantContains: "Notification sent via persistent_notification/create",
			wantHAPath:   "/api/services/persistent_notification/create",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastPath = ""
			r := &Registry{
				tools:         make(map[string]*Tool),
				ha:            tt.ha,
				notifyService: tt.service,
				notifyTitle:   tt.title,
			}

			result, err := r.handleSendMessage(context.Background(), tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantContains != "" && !strings.Contains(result, tt.wantContains) {
				t.Errorf("result = %q, want to contain %q", result, tt.wantContains)
			}
			if tt.wantHAPath != "" && lastPath != tt.wantHAPath {
				t.Errorf("HA path = %q, want %q", lastPath, tt.wantHAPath)
			}
		})
	}
}

func TestSetNotificationConfig_RegistersTool(t *testing.T) {
	r := NewEmptyRegistry()
	if r.Get("send_message") != nil {
		t.Fatal("send_message should not be registered before SetNotificationConfig")
	}

	r.SetNotificationConfig("notify/test", "Test Title")

	tool := r.Get("send_message")
	if tool == nil {
		t.Fatal("send_message should be registered after SetNotificationConfig")
	}
	if tool.Name != "send_message" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "send_message")
	}
}

func TestSetNotificationConfig_EmptyServiceNoRegister(t *testing.T) {
	r := NewEmptyRegistry()
	r.SetNotificationConfig("", "Title")

	if r.Get("send_message") != nil {
		t.Fatal("send_message should not be registered with empty service")
	}
}
