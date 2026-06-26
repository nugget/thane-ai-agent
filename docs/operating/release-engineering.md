# Release Engineering

Thane now has two operator paths on macOS:

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
just deploy-macos aimee@pocket.hollowoak.net
```

What it does:

- requires a clean checkout
- requires `THANE_NOTARY_PROFILE`
- builds a signed macOS installer package from the current branch
- notarizes and staples that package before deployment
- copies the `.pkg` to the remote host over SSH
- checks the package signature on the remote host
- installs it into `CurrentUserHomeDirectory`
- lets the target-side binary watcher react to the new install
- polls the remote Thane API until `/v1/version` reports the expected build version

This path assumes the remote host is also macOS and that Thane is run
from a dedicated account whose normal home-scoped install location is
`~/Thane/bin/thane`.

Required environment for this path:

- `THANE_CODESIGN_IDENTITY`
- `THANE_INSTALLER_IDENTITY`
- `THANE_NOTARY_PROFILE`

Useful variants:

```bash
just build-macos-pkg
just deploy-macos aimee@pocket.hollowoak.net arm64
just deploy-macos aimee@pocket.hollowoak.net arm64 0.9.0 /tmp/thane-releng
just deploy-macos aimee@pocket.hollowoak.net arm64 0.9.0 /tmp/thane-releng http://127.0.0.1:18080/v1/version 90
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

## Release Artifact Contract (the macOS auto-updater)

The [thane-agent-macos](https://github.com/nugget/thane-agent-macos) companion
app auto-updates its managed `thane` binary from this repo's GitHub releases.
Its updater (`LocalServer/UpdateManager.swift`) is the consumer and arbiter of
release integrity, so the following are a hard contract — breaking one bricks the
update path. `just ci`, `package-macos-pkg.sh`'s self-check, and the
`prepare-release`/`publish-release` guards enforce most of them; keep the rest in
mind when touching the release recipes.

- **Asset names** — each release exposes `thane_<version>_darwin_arm64.pkg` and
  `thane_<version>_darwin_amd64.pkg` (the updater matches the `darwin_<arch>.pkg`
  suffix), plus exactly one `thane_<version>_checksums.txt`. No second `.pkg` per
  arch and no second `*_checksums.txt` per release.
- **Checksums** — `checksums.txt` is `sha256sum`/`shasum -a 256` text-mode lines
  (`<hex>  <bare-basename>`), hashed *after* signing/notarizing/stapling. The
  entry name must equal the uploaded asset name exactly.
- **Pkg layout** — `pkgutil --expand-full` must yield
  `thane-component.pkg/Payload/Thane/bin/thane`; the binary stays named `thane`
  and installs to `~/Thane/bin/thane`. `package-macos-pkg.sh` self-checks this.
- **Signing** — production pkgs are Developer ID Installer-signed, notarized, and
  stapled. The updater records but does not reject an unsigned pkg, so
  `prepare-release`/`publish-release` fail fast without the signing credentials.
- **Version tags** — `vX.Y.Z[-pre]` with a three-integer core and **no
  `+build-metadata`** (the updater's `SemanticVersion` parser can't read it).
- **Release visibility** — published on `nugget/thane-ai-agent`, non-draft; a
  non-prerelease "latest" must exist for default-channel updaters.

The app updates *itself* (a `.dmg`) from the thane-agent-macos repo through a
parallel updater; that contract is arch-agnostic where this one is
arch-specific. Any future convergence of the two release pipelines must keep the
`.pkg` arch-qualified and the `.dmg` arch-agnostic.
