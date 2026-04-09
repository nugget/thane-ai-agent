# Release Engineering

Thane now has two operator paths on macOS Tahoe:

- a guarded GitHub release path for real published releases
- a pkg-based remote deploy path for live-host testing

Both flows are designed for the human who coordinates release work often.
They keep the high-level progress readable while still letting the underlying
tool output stream through in real time.

## Preferred Workflows

### Publish A GitHub Release

Use this when you are cutting a real release for GitHub, the macOS companion
app, and manual operator installs.

```bash
just release-github 0.9.0
just release-github 0.9.0 prerelease
```

What it does:

- requires a clean checkout
- requires `main`
- requires local `main` to match `origin/main`
- runs the normal CI gate
- builds the macOS installer packages and Linux tarballs
- signs, notarizes, and staples the macOS `.pkg` artifacts
- writes checksums
- smoke-tests the release container image locally
- creates or updates the GitHub release with either release or prerelease state

Required environment for this path:

- `THANE_CODESIGN_IDENTITY`
- `THANE_INSTALLER_IDENTITY`
- `THANE_NOTARY_PROFILE`
- `THANE_GH_TOKEN`

`release_kind` can be:

- `auto`
  - marks versions containing `-` as prereleases
- `prerelease`
  - always creates or updates a prerelease
- `release`
  - always creates or updates a full release

If you intentionally want a breakpoint between local artifact preparation and
GitHub publication, the lower-level building blocks still exist:

```bash
just prepare-release 0.9.0
just publish-release 0.9.0
```

Most operators should prefer `just release-github ...` instead.

### Deploy A Pkg To A Live macOS Host

Use this when you want to test the real installer path on a remote Thane host
without cutting a GitHub release.

```bash
just deploy-macos-pkg aimee@pocket.hollowoak.net
```

What it does:

- requires a clean checkout
- builds a signed macOS installer package from the current branch
- copies the `.pkg` to the remote host over SSH
- checks the package signature on the remote host
- installs it into `CurrentUserHomeDirectory`
- restarts the launch agent
- polls the remote Thane API until `/v1/version` reports the expected build version

This path assumes the remote host is also macOS Tahoe and that Thane is run
from a dedicated account whose normal home-scoped install location is
`~/Thane/bin/thane`.

Required environment for this path:

- `THANE_CODESIGN_IDENTITY`
- `THANE_INSTALLER_IDENTITY`

Useful variants:

```bash
just build-macos-pkg
just deploy-macos-pkg aimee@pocket.hollowoak.net arm64
just deploy-macos-pkg aimee@pocket.hollowoak.net arm64 0.9.0 /tmp/thane-releng 'launchctl kickstart -k gui/$(id -u)/info.nugget.thane'
just deploy-macos-pkg aimee@pocket.hollowoak.net arm64 0.9.0 /tmp/thane-releng '' http://127.0.0.1:18080/v1/version 90
```

## Why Pkg-Based Deploys

Even during development, Thane should exercise the same installation shape it
ships to operators:

- package metadata is inspectable with normal Apple tooling
- install domain stays home-scoped instead of requiring admin privileges
- `~/Thane/bin/thane` stays the single managed macOS binary location
- release engineering can converge on one trustworthy artifact type

Raw binary copies are still possible with lower-level tooling, but they are no
longer the preferred operational path.
