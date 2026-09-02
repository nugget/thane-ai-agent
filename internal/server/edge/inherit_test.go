package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestAdoptListenersEnvironmentContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		env       map[string]string
		pid       int
		wantNames []string
		wantErr   string
		wantWarn  string
	}{
		{"no LISTEN_FDS adopts nothing", map[string]string{}, 100, nil, "", ""},
		{"LISTEN_FDS zero adopts nothing", map[string]string{"LISTEN_FDS": "0", "LISTEN_PID": "100"}, 100, nil, "", ""},
		{"malformed LISTEN_FDS is an error", map[string]string{"LISTEN_FDS": "two"}, 100, nil, "not a non-negative integer", ""},
		{"foreign LISTEN_PID is ignored with a warning", map[string]string{"LISTEN_FDS": "1", "LISTEN_PID": "999", "LISTEN_FDNAMES": "https"}, 100, nil, "", "another process"},
		{"duplicate name is an error", map[string]string{"LISTEN_FDS": "2", "LISTEN_PID": "100", "LISTEN_FDNAMES": "https:https"}, 100, nil, "twice", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			got, err := adoptListeners(envFrom(tc.env), tc.pid, logger)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("adoptListeners: %v", err)
			}
			if len(got) != len(tc.wantNames) {
				t.Fatalf("adopted %v, want %v", got, tc.wantNames)
			}
			if tc.wantWarn != "" && !strings.Contains(buf.String(), tc.wantWarn) {
				t.Fatalf("log %q lacks %q", buf.String(), tc.wantWarn)
			}
		})
	}
}

// TestInheritListenersFromSupervisor spawns this test binary as the
// child, hands it two real listening sockets at descriptors 3 and 4 with
// the systemd environment, and reads back what it adopted. The duplicate
// names test above covers the parsing; this covers the descriptors.
func TestInheritListenersFromSupervisor(t *testing.T) {
	if os.Getenv("THANE_INHERIT_CHILD") == "1" {
		inheritChildMain()
		return
	}
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer https.Close()
	httpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpL.Close()
	stray, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stray.Close()
	files := make([]*os.File, 0, 3)
	for _, l := range []net.Listener{https, httpL, stray} {
		f, err := l.(*net.TCPListener).File()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		files = append(files, f)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestInheritListenersFromSupervisor$")
	cmd.ExtraFiles = files // land at fds 3, 4, 5 in the child
	cmd.Env = append(os.Environ(),
		"THANE_INHERIT_CHILD=1",
		"LISTEN_FDS=3",
		"LISTEN_FDNAMES=https:http:metrics",
	)
	// LISTEN_PID must be the child's pid, which we only know after start;
	// the contract tolerates its absence, and the parent-pid case is
	// covered by the environment table. Here we assert the descriptor path.
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	var report map[string]string
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("child output %q: %v", out, err)
	}
	if report["https"] != https.Addr().String() || report["http"] != httpL.Addr().String() {
		t.Fatalf("child adopted %v, want https=%s http=%s", report, https.Addr(), httpL.Addr())
	}
	if _, ok := report["metrics"]; ok {
		t.Fatalf("child adopted a descriptor under an unknown name: %v", report)
	}
	if report["env_cleared"] != "true" {
		t.Fatalf("child did not clear the LISTEN_* environment: %v", report)
	}
}

// inheritChildMain is the child half of the supervisor test: adopt, then
// report each listener's address as JSON on stdout.
func inheritChildMain() {
	listeners, err := InheritListeners(slog.New(slog.DiscardHandler))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report := map[string]string{}
	for name, l := range listeners {
		report[name] = l.Addr().String()
	}
	report["env_cleared"] = strconv.FormatBool(os.Getenv("LISTEN_FDS") == "" && os.Getenv("LISTEN_FDNAMES") == "")
	_ = json.NewEncoder(os.Stdout).Encode(report)
	os.Exit(0)
}

// TestInheritedListenersServeAndNameThePort starts the front door on
// inherited sockets and checks that the redirect names the inherited
// HTTPS port rather than anything in the config.
func TestInheritedListenersServeAndNameThePort(t *testing.T) {
	t.Parallel()
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, t.TempDir())
	cfg.HTTPS.Port = 8443
	cfg.HTTPS.PublicPort = 9999 // must lose to the inherited socket's port
	cfg.HTTP.Disabled = true    // an inherited http socket still serves
	s, err := New(Options{
		Config:    cfg,
		Surfaces:  testSurfaces(),
		Logger:    slog.New(slog.DiscardHandler),
		Listeners: map[string]net.Listener{InheritedHTTPS: https, InheritedHTTP: httpL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.PublicPort() != listenerPort(https) {
		t.Fatalf("public port = %d, want inherited %d", s.PublicPort(), listenerPort(https))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Start(ctx) }()
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = s.Shutdown(shutdownCtx)
	})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = client.Get("http://" + httpL.Addr().String() + "/v1/version")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("redirect listener never answered: %v", err)
	}
	defer resp.Body.Close()
	want := "https://127.0.0.1:" + strconv.Itoa(listenerPort(https)) + "/v1/version"
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != want {
		t.Fatalf("redirect = %d %q, want 308 %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}
