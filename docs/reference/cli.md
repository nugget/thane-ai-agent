# CLI Reference

Thane ships as a single binary with eight commands.

```
$ thane --help
Thane - Autonomous Home Assistant Agent

Usage: thane [flags] <command> [args]

Commands:
  serve        Start the API server
  init [dir]   Initialize working directory with defaults (default: .)
  validate     Parse and validate the config without starting services
  ask          Ask a single question (for testing)
  ingest       Import markdown docs into fact store
  caps         Show resolved capability tags from a running daemon
  health [url] Probe a running daemon's /health endpoint (exit 0 if healthy)
  version      Show version information

Flags:
  -workspace <dir>  Instance workspace (default: ~/Thane)
  -insecure-config <path>
                    Load config from outside the trust boundary,
                    for recovery. Not signature-verified.
  -o, --output fmt  Output format: text (default) or json
```

## Commands

### `thane serve`

Start the API server. This is the primary runtime mode — Thane runs as a
long-lived process serving APIs, processing events, and managing scheduled
tasks.

Starts these listeners:
- **Port 8080** — Native Thane /v1 API + web dashboard
- **Port 8081** — OpenAI-compatible API (optional)
- **Port 11434** — Ollama-compatible API (for Home Assistant)
- **Port 8843** — CardDAV server (for contact sync)

### `thane init [dir]`

Initialize a Thane working directory with bundled defaults. Creates the
directory structure (`db/`, `talents/`, `archive/`), writes a default
`config.yaml` (0600 permissions, contains placeholders for secrets) and a
default `persona.md`, deploys the embedded talent corpus, bootstraps the
core identity (signing key, channel CA) and the archive skeleton
(orientation READMEs + the `interactions/` schema stub). Existing files
are never overwritten — re-runs report `(exists, skipping)` per file, so
it's safe to run against an established workspace to fill in anything
missing.

`dir` defaults to the current directory.

```bash
thane init ~/Thane
```

### `thane validate`

Parse and validate the config file without starting any services or
opening any sockets. Useful as a pre-deploy gate (`thane validate &&
thane serve`) or in CI.

```bash
thane validate                            # auto-discovered config
thane -insecure-config /etc/thane/config.yaml validate
thane -o json validate | jq .             # structured report for scripting
```

Text mode prints a one-line confirmation plus a short structural summary
(default model, resource/model/root counts, MCP server count, which
optional integrations are configured), then the core integrity report and
per-root admission. JSON mode emits `{path, valid, error, summary,
integrity, root_admission}` and exits non-zero on failure.

Admission is reported for every git-backed root that declares seed
signers and verifies signatures, so a root whose history no declared key
can account for is visible before a deploy rather than after. A failing
root under `verify_signatures: required` exits non-zero, matching what
`serve` refuses over; a failing root under `warn` is reported without
affecting the exit code, matching what `serve` merely logs. See
[Document Roots](../understanding/document-roots.md) for what admission
checks and how to repair a refusal.

### `thane ask`

One-shot question for testing. Runs a single request through the agent loop
and prints the response. Uses in-memory stores — no persistent state.

```bash
thane ask "What time is it?"
```

### `thane ingest`

Import markdown documents into the semantic fact store. Parses structured
content into categorized facts with optional embeddings.

```bash
thane ingest ~/notes/home-layout.md
```

### `thane caps`

Show resolved capability tags from a running daemon — useful for
inspecting which tags resolved on the running config and what tools each
tag carries. Reads from the live `serve` process via its API; requires
the daemon to be running.

```bash
thane caps
thane caps -x          # include tags the operator overlay excluded
thane -o json caps     # structured output
```

### `thane health`

Probe a running daemon's `/health` endpoint and exit 0 if it reports
healthy, non-zero otherwise. Useful as a liveness check in scripts,
supervisors, or container health probes. Takes an optional URL; defaults
to the local daemon if omitted.

```bash
thane health
thane health http://127.0.0.1:8080/health
```

### `thane version`

Print version, commit hash, build time, and branch information. Version is
injected at build time via ldflags.

## Config Location

Thane loads its runtime configuration from exactly one place:

```
<workspace>/core/config.yaml
```

The workspace defaults to `~/Thane` and is set with `-workspace`. There is
no search path.

That is deliberate. The config decides what the rest of the system
trusts: it sets `verify_signatures`, names the allowed-signers source,
chooses model endpoints, and points every document root at a path. A
config found by probing several locations would make the trust anchor
depend on the working directory, and a file that cannot be named cannot
be verified. Living inside `core` means the config is git-tracked and
signed like everything else in the trust boundary.

`workspace.path` is derived from the config's own location rather than
declared. A config that declares one contradicting where it was loaded
from is rejected, since that disagreement means the instance's roots,
state, and identity would point somewhere other than the directory the
config came from.

If no config exists at the canonical path but one is found at a
pre-core location, Thane refuses to start and prints the exact commands
to move and commit it.

### Startup verification

`thane serve` refuses to start when the instance's core does not meet the
integrity requirements: core must be a git repository with commit
history, the config must be committed and covered by a signature from a
key listed in `.allowed_signers`, no private key material may be tracked,
and no tracked file may have uncommitted changes.

The refusal names each failing check and the command that fixes it, and
`thane validate` prints the same report without starting anything. The
gate applies to `serve` rather than every subcommand because `serve` is
what runs unattended.

`serve` also refuses when a document root's history is not admitted by
the seed signers declared for it — a separate question from core
integrity, covered under [Document
Roots](../understanding/document-roots.md). `validate` reports that too,
from the same code the gate uses, so the two cannot answer differently.

Signers resolve from core's own `.allowed_signers`. That is sufficient
while core has no remote — an attacker who can rewrite the signer list
already has local write access. It stops being sufficient once core syncs
from elsewhere, so an out-of-tree trust anchor is a prerequisite for
giving core a remote.

### Exit codes and supervisors

| Code | Meaning |
| ---- | ------- |
| 0 | success |
| 78 | terminal — retrying will not help (`EX_CONFIG`) |
| 1 | everything else, possibly transient |

The distinction a supervisor needs is not which subsystem failed but
whether waiting will help. An unreachable broker deserves a restart; a
core with an unsigned config will fail identically forever, and a
supervisor that keeps restarting it turns one clear error into an endless
stream of them.

Exit 78 covers a missing or invalid config, a core that fails
verification, and a malformed command line. The refusal is also written
as a structured log record with `failed_checks` and `exit_code` fields,
so a log-scraping supervisor gets the failing check names without parsing
the human-readable message.

**systemd** — stop the restart loop on a failure a restart cannot fix:

```ini
[Service]
ExecStartPre=/usr/local/bin/thane -workspace /var/lib/thane validate
ExecStart=/usr/local/bin/thane -workspace /var/lib/thane serve
Restart=on-failure
RestartPreventExitStatus=78
```

**Docker** — `thane validate` is the health probe, since it runs the same
checks `serve` gates on without starting anything:

```dockerfile
HEALTHCHECK --start-period=30s CMD thane -workspace /data validate || exit 1
```

**Any supervisor** — `thane validate -o json` reports the whole picture
in one object: `valid` for the config, and `integrity.checks[]` with a
`status` and a `fix` per check. Surfacing `fix` verbatim is usually
better than paraphrasing it, since it is already the command an operator
would run.

`thane validate` fails whenever `thane serve` would refuse, so
`thane validate && thane serve` is a real guard rather than a courtesy.

### Recovery

`-insecure-config <path>` loads a config from an exact path, bypassing
the workspace. It exists for recovery and debugging when the canonical
config cannot be loaded — a rotated key, a broken core repository.

An instance started this way runs with capabilities withheld, because a
configuration nobody can be shown to have authorized should not be able
to act on the world as though someone had:

- **Tools that contact a human directly are refused** — Signal, email,
  notifications, escalations. The refusal explains the state rather than
  reporting the tool as missing, so the model reports its findings in its
  reply instead of looking for another route to the same person.
- **Service loops do not start automatically.** Unattended work needs a
  verified instance; an operator can still launch a specific loop
  deliberately.
- **`/health` reports `status: degraded` with `trust: unverified`**, so a
  supervisor cannot mistake a recovery session for a healthy instance
  left running indefinitely.
- **The model is told**, in its own context, that its configuration is
  unattested and which capabilities are withheld — before it plans around
  having them.

The name is literal rather than cautionary. Verification means a file is
covered by the instance's signed core history, which a file outside core
cannot be; so a config loaded this way is insecure by construction, not
merely unchecked. The flag says so before it is typed rather than after
it is diagnosed.

`-config` was the previous name and is now rejected with a message
pointing at `-workspace` for the ordinary case.

