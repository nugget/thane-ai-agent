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

Thane listens on these ports (all configurable):

| Port | Service | Required |
|------|---------|----------|
| 8080 | Native API + web dashboard | Yes |
| 11434 | Ollama-compatible API (for HA) | Yes |
| 8081 | OpenAI-compatible API | Optional |
| 8843 | CardDAV server | Optional (contact sync) |
| 443 | HTTPS front door | Optional (`tls.enabled`) |
| 80 | HTTP to HTTPS redirect | Optional (with the front door) |

The HTTPS front door terminates TLS in-process and routes each configured
hostname to one of the plaintext surfaces above, so no reverse proxy is
needed; see [HTTPS Front Door](configuration.md#https-front-door).

Ports 80 and 443 need privilege on every platform Thane runs on, and
Thane should never hold it. The front door therefore accepts listening
sockets a supervisor bound as root and handed down under the systemd
socket-activation contract: descriptors from 3 up, `LISTEN_FDS` saying how
many, `LISTEN_PID` naming the intended child, and `LISTEN_FDNAMES` naming
each one. A descriptor named `https` replaces the TLS bind and one named
`http` replaces the redirect bind; an absent name binds the configured
port as usual, and the redirect names the inherited socket's own port.
Thane clears the variables after adopting the sockets and marks every
handed-down descriptor close-on-exec so no tool subprocess inherits one.
Because the supervisor still owns the listening socket, connection
attempts that arrive during a restart wait in its backlog for the new
process instead of being refused; connections already established with
the old process still close with it.

On Linux that is one socket unit per name, both tied to the service:

```ini
# thane-https.socket
[Socket]
ListenStream=443
FileDescriptorName=https
Service=thane.service

[Install]
WantedBy=sockets.target
```

```ini
# thane-http.socket
[Socket]
ListenStream=80
FileDescriptorName=http
Service=thane.service

[Install]
WantedBy=sockets.target
```

with `Requires=thane-https.socket thane-http.socket` in the service;
systemd binds both ports as root and starts Thane unprivileged with the
environment above, `LISTEN_PID` included. Granting
the binary `CAP_NET_BIND_SERVICE` or lowering
`net.ipv4.ip_unprivileged_port_start` also works, at the cost of privilege
in the process.

macOS refuses ports below 1024 to ordinary users, admin or not, and offers
no capability to grant. The supported path is the companion app's port
broker: a LaunchDaemon the app registers through `SMAppService`, whose
plist declares `Sockets` for 443 and 80 so launchd binds them at boot and
hands them to the app, which starts Thane with the same environment
(`LISTEN_PID` set from inside the child by a shell that execs Thane in
place, since the app cannot know the pid before the spawn). On a
Mac without the companion, keep Thane on high ports and let the packet
filter carry the public ones instead: bind `tls.https.port: 8443` and
`tls.http.port: 8880`, set `tls.https.public_port: 443` so the redirect
names the port clients use, and install a `pf` redirect once as root, for
example an anchor containing

```
rdr pass on en0 inet proto tcp from any to any port 443 -> 127.0.0.1 port 8443
rdr pass on en0 inet proto tcp from any to any port 80  -> 127.0.0.1 port 8880
```

and reference it from `/etc/pf.conf` so it survives a reboot:

```
rdr-anchor "thane"
load anchor "thane" from "/etc/pf.anchors/thane"
```

Adjust the interface name to the one carrying LAN traffic, load with
`pfctl -f /etc/pf.conf` and enable with `pfctl -e`, and verify by naming
the anchor, since `pfctl -s nat` alone does not descend into anchors:
`pfctl -a thane -s nat`.

### Replacing a reverse proxy with the front door

Bring the front door up beside the proxy rather than in place of it.
Configure `tls:` with `certmagic.ca` set to the Let's Encrypt staging
directory and `https.port` on an alternate port such as 8443, then start
Thane and watch `subsystem=tls` log lines for `tls certificate obtained`
against every hostname. Staging certificates are not browser-trusted, so
verify with `curl --insecure` or by inspecting the issuer. Once every
hostname issues, remove `certmagic.ca`, move the front door onto the public
ports, stop the proxy, and restart Thane; the hostnames already point at
the host, so no DNS change is involved. Moving onto the public ports means
`https.port: 443` and `http.port: 80` only where Thane has been granted
the privilege to bind them (the Linux capability above); on macOS, and on
any host where Thane stays unprivileged, keep the high binds and set
`https.public_port: 443` with the packet-filter redirect above carrying
443 and 80. Thane's own access log now records real client addresses where
the proxy reported its own.

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
