package edge

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Inherited listener names the front door recognises in LISTEN_FDNAMES.
// A descriptor under any other name is left alone for whoever it was
// meant for.
const (
	InheritedHTTPS = "https"
	InheritedHTTP  = "http"
)

// listenFDStart is the first descriptor the sd_listen_fds(3) contract
// hands over; 0, 1, and 2 stay the standard streams.
const listenFDStart = 3

// InheritListeners adopts listening sockets a supervisor pre-bound and
// passed down under the systemd socket-activation contract, which both
// init systems Thane runs under can speak: descriptors from 3 up,
// LISTEN_FDS saying how many, LISTEN_PID naming the intended child (it
// must equal this process's pid, or nothing is adopted), and
// LISTEN_FDNAMES naming each one. Ports 80 and 443 need privilege Thane must never
// hold; this is how the operating system keeps it while Thane serves.
//
// Only the names the front door knows are adopted, keyed by name in the
// result. The environment is unset afterwards, as sd_listen_fds does by
// default, and each adopted descriptor is marked close-on-exec so the
// listener never leaks into a tool subprocess. An environment with no
// LISTEN_FDS returns an empty map and touches nothing.
func InheritListeners(logger *slog.Logger) (map[string]net.Listener, error) {
	if logger == nil {
		logger = slog.Default()
	}
	env := func(k string) string { return os.Getenv(k) }
	listeners, err := adoptListeners(env, os.Getpid(), logger)
	for _, k := range []string{"LISTEN_FDS", "LISTEN_PID", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(k)
	}
	return listeners, err
}

// adoptListeners is InheritListeners with its inputs injectable.
func adoptListeners(env func(string) string, pid int, logger *slog.Logger) (map[string]net.Listener, error) {
	listeners := map[string]net.Listener{}
	rawCount := strings.TrimSpace(env("LISTEN_FDS"))
	if rawCount == "" {
		return listeners, nil
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("edge: LISTEN_FDS %q is not a non-negative integer", rawCount)
	}
	if count == 0 {
		return listeners, nil
	}
	// The contract requires LISTEN_PID to name this process. sd_listen_fds
	// treats a missing or foreign pid as "not for us" and adopts nothing,
	// and so does this: a stale environment with live descriptors at 3
	// and up must not be mistaken for a handoff.
	rawPID := strings.TrimSpace(env("LISTEN_PID"))
	if intended, err := strconv.Atoi(rawPID); err != nil || intended != pid {
		logger.Warn("ignoring inherited listeners not addressed to this process",
			"listen_pid", rawPID, "pid", pid, "listen_fds", count)
		return listeners, nil
	}
	// Every descriptor in the handed-down range is marked close-on-exec
	// before any is looked at, as sd_listen_fds does, so an unnamed or
	// unknown one cannot leak into a tool subprocess while it waits for
	// an in-process consumer.
	for i := 0; i < count; i++ {
		syscall.CloseOnExec(listenFDStart + i)
	}
	names := strings.Split(env("LISTEN_FDNAMES"), ":")
	// Resolve every name before touching a single descriptor, so a
	// malformed handoff adopts nothing rather than half of something.
	type handoff struct {
		fd   int
		name string
	}
	var adopt []handoff
	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		fd := listenFDStart + i
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		switch name {
		case InheritedHTTPS, InheritedHTTP:
		default:
			logger.Debug("leaving inherited descriptor alone", "fd", fd, "name", name)
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("edge: LISTEN_FDNAMES names %q twice", name)
		}
		seen[name] = true
		adopt = append(adopt, handoff{fd: fd, name: name})
	}
	for _, h := range adopt {
		file := os.NewFile(uintptr(h.fd), h.name)
		if file == nil {
			return nil, fmt.Errorf("edge: inherited descriptor %d (%s) is not open", h.fd, h.name)
		}
		l, err := net.FileListener(file)
		// FileListener duplicates the descriptor; the original stays
		// owned by the *os.File, which we close so exactly one handle
		// remains.
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("edge: inherited descriptor %d (%s) is not a listening socket: %w", h.fd, h.name, err)
		}
		listeners[h.name] = l
		logger.Info("front door inherited a listener from the supervisor", "name", h.name, "address", l.Addr().String())
	}
	return listeners, nil
}

// listenerPort returns the TCP port a listener is bound to, or 0 when it
// is not a TCP listener.
func listenerPort(l net.Listener) int {
	if addr, ok := l.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}
