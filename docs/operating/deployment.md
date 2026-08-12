# Deployment

Thane ships as a single Go binary that runs against a workspace. The
workspace is the deployable unit: `thane init` bootstraps it, the config
lives inside it at `<workspace>/core/config.yaml` under signed version
control, and `thane serve -workspace <dir>` (default `~/Thane`) is the
one process a supervisor needs to keep alive. Go's cross-compilation
means Thane runs anywhere — macOS and Linux are the tested platforms.

## macOS

For production use, create a dedicated macOS user account for Thane
(standard or administrator). This keeps Thane's data, config, and runtime
isolated from your personal account. Everything lives under `~/Thane/` in
that user's home directory — Finder-visible, easy to inspect and back up.

The [companion app](#macos-companion-app) owns the process lifecycle for
production macOS deployments. To run an instance by hand:

```bash
just install                  # -> ~/Thane/bin/thane
just init ~/Thane             # Bootstrap the workspace (core trust root, signed config)
~/Thane/bin/thane serve       # Loads ~/Thane/core/config.yaml; -workspace points elsewhere
just logs ~/Thane             # Tail the latest ~/Thane/archive/sources/thane/events/... JSONL segment
just alerts ~/Thane WARN 2    # Follow new WARN+ records across every structured log dataset
```

`just logs` is the broad operational event stream. `just alerts` reads the
structured SQLite index and follows only new warnings and errors, including
agent-request records that live in the requests dataset rather than events.
It emits one compact JSON object per line, so ordinary filters compose cleanly:

```bash
just alerts ~/Thane WARN 2 | grep 'illegal tool call'
just alerts ~/Thane ERROR 2 | jq .
```

### macOS Local Network Permission

You **must** grant Local Network permission in System Settings > Privacy &
Security > Local Network for Thane to access LAN services like Home
Assistant and Ollama.

macOS silently blocks unsigned binaries from accessing LAN hosts. This was
a tricky diagnosis — see
[issue #53](https://github.com/nugget/thane-ai-agent/issues/53). The
justfile ad-hoc signs macOS builds (`codesign -s -`) to reduce friction,
and the release recipes can use a Developer ID Application certificate,
a Developer ID Installer certificate, `notarytool`, and `stapler` when
`THANE_CODESIGN_IDENTITY`, `THANE_INSTALLER_IDENTITY`, and
`THANE_NOTARY_PROFILE` are configured. The Local Network permission still
needs manual approval.

### macOS Companion App

The native macOS companion app at
[thane-agent-macos](https://github.com/nugget/thane-agent-macos) is the
production supervisor on macOS — it manages the Thane process, handles
permissions, applies auto-updates, and provides a native menu bar
presence. `just deploy-macos user@host` builds, notarizes, and installs
a signed pkg on a companion-managed host.

## Linux

Thane ships no supervisor unit: it is one static binary plus a workspace,
so it slots into whatever supervision the host already runs. Give the
service its own user, put the workspace in that user's territory, and
point the supervisor at `thane serve -workspace`:

```bash
sudo useradd --system --create-home --home-dir /var/lib/thane --shell /usr/sbin/nologin thane
sudo just install                              # -> /usr/local/bin/thane
sudo -u thane /usr/local/bin/thane init /var/lib/thane
```

A minimal systemd unit is a few lines:

```ini
[Unit]
Description=Thane
After=network-online.target
Wants=network-online.target

[Service]
User=thane
ExecStart=/usr/local/bin/thane serve -workspace /var/lib/thane
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

The config lives at `/var/lib/thane/core/config.yaml`, inside the
workspace's signed history — edit and commit it there (there is no
`/etc/thane`). `thane validate` reports anything the boot gate would
refuse, and `just alerts /var/lib/thane` follows warnings and errors
from the workspace's structured log index.

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

Release artifacts are prepared on a local macOS release workstation with the
`just` release recipes. That keeps Developer ID signing, installer packaging,
Apple notarization, and stapling in local control while still producing Linux
`amd64` and `arm64` artifacts for GitHub Releases. The macOS artifacts are
flat installer product archives that install `thane` into `~/Thane/bin` for
the current macOS account, advertise the intended CPU family to Installer,
avoid a machine-wide admin prompt, and carry first-party
welcome/readme/license metadata for inspection. The tagged GitHub workflow
publishes the multi-arch container image and its provenance attestation.

For the human-facing operator workflows, see
[Release Engineering](release-engineering.md). The preferred paths are:

- `just release-github <version>` for real GitHub releases from a clean main checkout
- `just deploy-macos user@host` for pkg-based live-host testing on another macOS system

## Container

Thane also ships as a multi-arch container image on GHCR:

```bash
docker run --rm \
  -p 8080:8080 \
  -p 11434:11434 \
  -v "$PWD/config:/config" \
  -v thane-data:/data \
  ghcr.io/nugget/thane-ai-agent:latest serve
```

Container conventions:

- Put your config at `/config/config.yaml`
- The container runs with `/data` as its working directory, so the default `data_dir: ./db` persists to `/data/db`
- If you override `data_dir`, set it explicitly to a path under `/data` if you want state on the mounted volume
- Ensure the mounted `/data` path is writable by the non-root `thane` user before first boot
- Publish `8080` for the native API and dashboard
- Publish `11434` for the Ollama-compatible Home Assistant endpoint
