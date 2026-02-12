# Getting Started with Thane

## Prerequisites

- **[Go](https://go.dev/) 1.24+** (for building from source)
- **[just](https://just.systems/)** (command runner — replaces Makefiles)
- **[Ollama](https://ollama.ai/)** running with at least one model pulled
- **Home Assistant** (optional but recommended)

## Build

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build
```

This builds a platform-specific binary into `dist/`. Cross-compile with `just build linux arm64`.

## Initialize

Set up the `~/Thane` directory with config, talents, and persona:

```bash
just init
```

This creates `~/Thane/config.yaml`, copies talent files, and sets up the persona. Edit `~/Thane/config.yaml` for your setup.

At minimum, set:
- `models.ollama_url` — where your Ollama instance lives
- `models.default` — which model to use
- `data_dir` — where to store SQLite databases

If you have Home Assistant:
- `homeassistant.url` — your HA instance URL
- `homeassistant.token` — a long-lived access token ([how to create one](https://www.home-assistant.io/docs/authentication/#your-account-profile))

Optional cloud model support:
- `anthropic.api_key` — enables Claude models for complex reasoning

See `examples/config.example.yaml` for all available options with documentation.

## Run

```bash
# Start the server
just serve

# Or run the binary directly
~/Thane/bin/thane serve

# Quick test from CLI
~/Thane/bin/thane ask "Hello!"
```

The server starts two API endpoints:
- `http://localhost:8080` — Native API (OpenAI-compatible)
- `http://localhost:11434` — Ollama-compatible API (for HA integration)

## Web Chat

Thane includes a built-in web chat interface at `http://localhost:8080/chat`.

## Development

All workflows go through [just](https://just.systems/). Run `just` with no arguments to see available recipes:

```
$ just
Available recipes:

    [build]
    build target_os=host_os target_arch=host_arch  # Build a binary into dist/
    build-all                                       # Build for all release targets
    clean                                           # Clean build artifacts
    version                                         # Build and show version

    [test]
    ci                                              # CI: format check, lint, and tests
    fmt-check                                       # Check formatting
    lint                                            # Run linter
    test                                            # Run tests (always with race detector)

    [deploy]
    init                                            # Initialize ~/Thane with config and talents
    install                                         # Install the binary
    service-install                                 # Install and enable the system service
    service-uninstall                               # Remove the system service
    uninstall                                       # Uninstall the binary

    [operations]
    logs                                            # Tail live service logs
    service-status                                  # Show service status

    [release-engineering]
    release tag                                     # Tag and publish a GitHub release
```

### Common workflows

```bash
just build                    # Build for current platform → dist/
just build linux arm64        # Cross-compile
just test                     # Run tests (always with -race)
just ci                       # Full CI gate: fmt + lint + test
just install                  # Install binary to ~/Thane/bin/
just service-install          # Install + enable as system service
just logs                     # Tail live logs
```

## Deployment

Thane ships with production-grade service definitions. Go's cross-compilation means Thane runs anywhere — these are the platforms with tested service configs today:

### macOS

User launch agent, zero sudo required.

For production use, create a dedicated macOS user account for Thane (standard or administrator). This keeps Thane's data, config, and runtime isolated from your personal account. Everything lives under `~/Thane/` in that user's home directory — Finder-visible, easy to inspect and back up.

```bash
just install                  # → ~/Thane/bin/thane
just service-install          # → ~/Library/LaunchAgents/info.nugget.thane.plist
launchctl load ~/Library/LaunchAgents/info.nugget.thane.plist
just logs                     # Tail ~/Thane/thane.log
```

> **macOS note:** You must grant Local Network permission in System Settings → Privacy & Security → Local Network for Thane to access LAN services like Home Assistant. See [issue #53](https://github.com/nugget/thane-ai-agent/issues/53).

### Linux

systemd with dedicated service user and full security hardening:

```bash
sudo just install             # → /usr/local/bin/thane
sudo just service-install     # Creates thane user, installs unit, enables service
sudo cp examples/config.example.yaml /etc/thane/config.yaml
# Edit /etc/thane/config.yaml with your settings
sudo systemctl start thane
```

The systemd unit includes comprehensive sandboxing: `ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, `MemoryDenyWriteExecute`, `SystemCallFilter`, and more.

## Next Steps

- [Home Assistant Integration](homeassistant.md) — Connect Thane as your HA conversation agent
- [ARCHITECTURE.md](../ARCHITECTURE.md) — Understand how Thane works under the hood
- [config.example.yaml](../examples/config.example.yaml) — Full configuration reference
