# Deployment

Thane ships as a single Go binary with production-grade service definitions
for macOS and Linux. Go's cross-compilation means Thane runs anywhere —
these are the platforms with tested service configs today.

## macOS

User launch agent — zero sudo required.

For production use, create a dedicated macOS user account for Thane
(standard or administrator). This keeps Thane's data, config, and runtime
isolated from your personal account. Everything lives under `~/Thane/` in
that user's home directory — Finder-visible, easy to inspect and back up.

```bash
just install                  # -> ~/Thane/bin/thane
just service-install          # -> ~/Library/LaunchAgents/info.nugget.thane.plist
launchctl load ~/Library/LaunchAgents/info.nugget.thane.plist
just logs                     # Tail ~/Thane/thane.log
```

### macOS Local Network Permission

You **must** grant Local Network permission in System Settings > Privacy &
Security > Local Network for Thane to access LAN services like Home
Assistant and Ollama.

macOS silently blocks unsigned binaries from accessing LAN hosts. This was
a tricky diagnosis — see
[issue #53](https://github.com/nugget/thane-ai-agent/issues/53). The
justfile ad-hoc signs macOS builds (`codesign -s -`) to reduce friction,
but the Local Network permission still needs manual approval.

### macOS Companion App

A native macOS companion app is in early development at
[thane-agent-macos](https://github.com/nugget/thane-agent-macos). The
companion app aims to simplify deployment on macOS — managing the Thane
process, handling permissions, and providing a native menu bar presence.

## Linux

systemd with dedicated service user and full security hardening:

```bash
sudo just install             # -> /usr/local/bin/thane
sudo just service-install     # Creates thane user, installs unit, enables service
sudo cp examples/config.example.yaml /etc/thane/config.yaml
# Edit /etc/thane/config.yaml with your settings
sudo systemctl start thane
```

The systemd unit includes comprehensive sandboxing:
- `ProtectSystem=strict` — read-only filesystem except allowed paths
- `NoNewPrivileges` — no privilege escalation
- `PrivateTmp` — isolated temp directory
- `MemoryDenyWriteExecute` — W^X enforcement
- `SystemCallFilter` — restricted syscalls
- Dedicated user with no login shell

## Network Requirements

Thane listens on three ports (configurable):

| Port | Service | Required |
|------|---------|----------|
| 8080 | Native API + web dashboard | Yes |
| 11434 | Ollama-compatible API (for HA) | Yes |
| 8843 | CardDAV server | Optional (contact sync) |

Thane also needs outbound access to:
- Your Home Assistant instance (REST + WebSocket)
- Your MQTT broker
- Your Ollama instance (if on a different host)
- Anthropic API (if cloud models are configured)

## Cross-Compilation

Build for any supported target:

```bash
just build linux arm64        # Linux on ARM (e.g., Raspberry Pi 4)
just build linux amd64        # Linux on x86
just build darwin arm64       # macOS Apple Silicon
just build darwin amd64       # macOS Intel
just build-all                # All release targets
```

## Future: Docker

For operators who don't already run Home Assistant, a future release will
include a managed HA instance in a Docker container alongside Thane.
This is on the roadmap but not yet implemented.
