# Release Checklist

Every version bump follows this checklist. The version bump PR includes all of these items.

## Documentation Audit

- [ ] **README.md** — Accurate description of current capabilities
- [ ] **ARCHITECTURE.md** — Reflects actual implementation, roadmap phases updated
- [ ] **config.example.yaml** — Includes all current config options with documentation
- [ ] **docs/** — Getting started, integration guides reflect current reality
- [ ] **CONTRIBUTING.md** — Still accurate for current development workflow

## Code Quality

- [ ] All tests pass (`go test ./...`)
- [ ] Linting clean (`gofmt`, `golangci-lint`)
- [ ] No stale files in repo root (dead configs, temp files, etc.)
- [ ] Version string injected correctly (uses build-time `ldflags`, not hardcoded)

## Release Notes

- [ ] PR description includes summary of notable changes since last version
- [ ] GitHub Issues referenced where applicable

## Release Engineering

- [ ] Tag release and create GitHub Release (`just release X.Y.Z`)
- [ ] Verify release notes on GitHub (edit auto-generated notes as needed)

## Future Additions

_These will be added to the checklist as the project matures:_

- [ ] Build and publish Docker container image (multi-arch)
- [ ] HA Add-on version bump
