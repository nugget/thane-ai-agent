# Release Checklist

Use this for real release work, not just version bumps. The goal is to
ship a build that is technically sound, operationally inspectable, and
easy to roll back.

## Repo Preflight

- [ ] `just ci`
- [ ] `just build`
- [ ] `just release-build-snapshot <version>` for at least the primary operator target
- [ ] Generated files are committed, especially [`examples/config.example.yaml`](../examples/config.example.yaml)
- [ ] New exported Go types/functions read cleanly in Go Doc form
- [ ] Logs for new code paths use inherited/component loggers instead of ad hoc `slog.Default()` where request or subsystem context matters

## Artifact Publication

- [ ] `just prepare-release <version>` completes on the macOS release workstation
- [ ] Prepared release metadata matches the commit being published
- [ ] Release tag created with `just publish-release <version>` from a clean, up-to-date `main`
- [ ] Local release upload publishes the two macOS `.pkg` files, the two Linux tarballs, and a version-scoped `thane_<version>_checksums.txt` to the GitHub release
- [ ] GitHub release workflow publishes the GHCR multi-arch container image
- [ ] GitHub build provenance attestations are available for the container image
- [ ] GitHub release token is set explicitly (`THANE_GH_TOKEN`) for tag creation and release upload
- [ ] macOS release signing credentials are set (`THANE_CODESIGN_IDENTITY`) if shipping a Developer ID-signed build
- [ ] macOS installer signing credentials are set (`THANE_INSTALLER_IDENTITY`) if shipping signed `.pkg` artifacts
- [ ] macOS notarization profile is set (`THANE_NOTARY_PROFILE`) if shipping a notarized/stapled macOS installer package
- [ ] macOS installer packages inspect cleanly (`pkgutil --check-signature`, distribution metadata, current-user-home install domain, and architecture gating all match the intended release)

## Documentation Audit

- [ ] **README.md** — Accurate description of current capabilities
- [ ] **docs/understanding/** — Architecture, philosophy, and design docs reflect actual implementation
- [ ] **docs/operating/** — Getting started, integration, and deployment guides reflect current reality
- [ ] **docs/reference/** — Tools, CLI, and API docs accurate
- [ ] **config.example.yaml** — Includes all current config options with documentation
- [ ] **CONTRIBUTING.md** — Still accurate for current development workflow

## Config and Migration Review

- [ ] Start from the repo example config instead of editing an old production file in place
- [ ] Remove stale config-owned tool lists when compiled defaults or MCP `default_tags` can own membership instead
- [ ] Review document roots intentionally:
  - [ ] `paths.core` for persona/ego/metacognitive and other high-integrity docs
  - [ ] `paths.kb` for curated knowledge
  - [ ] `paths.generated` for model-produced durable artifacts
  - [ ] `paths.scratchpad` for low-integrity writable work
- [ ] Keep `data_dir` separate from document roots
- [ ] Back up the existing production config and binary before swapping anything

## Model and Tooling Sanity

- [ ] `/v1/version` reports the intended commit after restart
- [ ] `11434` exposes only the intended virtual model suite
- [ ] The intended premium/ops/assist semantics still match operator expectations
- [ ] Dynamic model-registry overlays are reviewed for any temporary canary-only policy changes
- [ ] MCP servers needed in production start successfully and their tools land in the intended tags/toolboxes

## Canary Smoke Tests

- [ ] One plain `8080` chat request succeeds
- [ ] One `11434` `thane:premium` request succeeds
- [ ] One tool-using request succeeds on the route you actually expect operators to use
- [ ] One Home Assistant request succeeds if HA is production-critical
- [ ] One scheduler/background loop completes after restart
- [ ] Dashboard, request viewer, and registry windows load cleanly enough for incident response

## Log Review

- [ ] Check recent `WARN`/`ERROR` output after restart
- [ ] Distinguish startup burst noise from steady-state regressions
- [ ] Remove or explain stale config warnings instead of normalizing them away
- [ ] Confirm request, loop, tool, and model fields are present on new operational log lines

## Rollback Readiness

- [ ] Previous binary is still available locally on the host
- [ ] Previous config is backed up and easy to restore
- [ ] Restart path uses the real supervisor in production, not vestigial install-time service scripts
- [ ] Rollback steps are short enough to execute under pressure

## Release Notes

- [ ] PR description summarizes the user-visible changes and operational implications
- [ ] Known alpha/canary risks are written down explicitly
- [ ] Follow-up cleanup items are split from release-blocking fixes
