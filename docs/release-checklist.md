# Release Checklist

Use this for real release work, not just version bumps. The goal is to
ship a build that is technically sound, operationally inspectable, and
easy to roll back.

The phases are time-ordered: audits land first, while a fix is still
cheap; assets get cut once the tree is clean; validation and
communication happen against the live build. Skipping forward is fine,
working backward is the smell.

## 1. Pre-release audits

Done before any artifacts are cut. This is where structural problems
get fixed cheaply — a redundant field collapsed here is one PR; the
same fix after a release ships is migration work.

- [ ] **Code-path audit** — sweep recent surfaces for redundant fields,
      parallel implementations, and dead code paths. The Godoc
      compliance audit below is the main lens for spotting these;
      apply it ruthlessly.
- [ ] **Godoc compliance audit**

  Godoc is a design check, not a documentation chore. Best-in-class
  Godoc forces every exported type, field, and function to justify
  its existence at the narrative level — what role it plays, why the
  abstraction exists, when callers should reach for it. Code that
  produces correct output but cannot explain its place in the system
  is incomplete.

  Two practical tests:

  - [ ] **Stand-alone test.** Each field's Godoc reads cleanly without
        referencing sibling fields for disambiguation. If `FieldA`
        needs "but if `FieldB` is also set, this is ignored" or
        "spec-defined consumers should use `FieldB` instead," the
        design is wrong — collapse, rename, or split until each
        field's purpose is independently statable.
        [#812](https://github.com/nugget/thane-ai-agent/issues/812) /
        [#813](https://github.com/nugget/thane-ai-agent/pull/813) is
        the canonical example: two tag fields whose honest Godocs
        could not be written without referencing each other, which
        was the smell that prompted the structural fix.
  - [ ] **Why-it-exists test.** Each Godoc answers not just *what*
        but *why*. "X is the user's display name" is not enough; "X
        is the operator-configured display name; the UI renders this
        and falls back to the email local-part when unset" is. If
        you can only describe the *what*, the abstraction has no
        narrative reason to exist as a distinct thing — fold it into
        whatever does have a reason.

  What to audit:

  - [ ] **Exported types** — what they represent, what role they
        play in the system, who constructs them, who consumes them
  - [ ] **Exported fields** — what they contain, when they are set,
        when they are consumed, what an empty/zero value means
  - [ ] **Exported functions/methods** — what they do, when callers
        should reach for them versus alternatives, what guarantees
        they make
  - [ ] **Recently added/changed surfaces especially** — fresh code
        is where narrative drift sneaks in before review is cold
  - [ ] **Cross-type lifecycle narratives** — when a field's role
        only makes sense alongside fields on other types (a value
        carried through Spec → Launch → Request, an override layered
        across packages), per-field Godoc breaks down. The right
        place is a `doc.go` or package comment that explains the
        whole lifecycle in one place; per-field comments then
        anchor to it instead of re-explaining the chain. The first
        audit run flagged the tag layering across `Spec.Tags`,
        `Launch.InitialTags`, `Request.InitialTags`, and
        `Request.RuntimeTags` as the canonical example.

  What this is *not*:

  - Not a comment-density gate. Internal symbols and self-evident
    getters need no commentary.
  - Not "explain WHAT the code does" — well-named identifiers do
    that. Explain WHY this exists and HOW it fits.
  - Not a place to log historical decisions ("added for issue X",
    "used by handler Y") — that belongs in PR descriptions and git
    history, not in the source.
- [ ] **Model-facing context audit**

  Every output that becomes system prompt content, capability context,
  delegate bootstrap context, tool output, summary scaffolding, or any
  other loop input has a model as its audience. Audit new and altered
  context-injection points against
  [docs/model-facing-context.md](model-facing-context.md). Cognitive
  clarity is expensive; typing is free.

  Run the litmus questions on each new/altered surface:

  - [ ] **What work is the model still being forced to do that Go
        could do first?** Timestamp arithmetic, unit conversion,
        schema inference, hidden-default recovery, scope inference
        from vague names — none of these belong in the model's path
        if Go can derive them. Pre-compute relationships, normalize
        values, pre-join related fields.
  - [ ] **Is this shape optimized for a model, or only for a human
        maintainer?** Generated runtime state defaults to typed JSON,
        not narrative prose. Markdown is for section boundaries,
        brief framing notes, and normative instructions; structured
        data is data.
  - [ ] **Does this belong in always-on context, capability context,
        a tool result, or nowhere at all?** Tag-scoped providers exist
        for a reason — every block that lives in always-on context
        thins what's left for the conversation.
  - [ ] **If this data changes often, why is it static?** Live config,
        active capabilities, recent tool activity, and external state
        belong in dynamic providers, not in markdown files frozen at
        build time.

  Anti-patterns to grep for explicitly:

  - [ ] Raw absolute timestamps in recency-sensitive context — use
        the delta helpers in
        [`internal/model/promptfmt/timefmt.go`](../internal/model/promptfmt/timefmt.go).
  - [ ] Essay-style markdown rendering data that should be a compact
        JSON projection.
  - [ ] Field names, section names, or ordering drifting between calls
        when stability would help the model compare turns; silent
        truncation rather than an explicit truncation marker.
  - [ ] Dumping raw upstream payloads where a smaller projection
        would do.
  - [ ] The same fact rendered in conflicting shapes across two
        injection points.
  - [ ] Instructions hiding inside what claims to be data, or runtime
        data baked into prompt prose that should be a context provider.

  Placement check:

  - [ ] Assembly and section ordering live in `internal/runtime/agent`.
  - [ ] Cross-domain time and recency helpers live in
        `internal/state/awareness` and `internal/model/promptfmt`.
  - [ ] Domain-to-view projection lives in the domain package as a
        co-located `contextfmt` subpackage with clean exported entry
        points. The canonical shape is
        [`internal/integrations/homeassistant/contextfmt/`](../internal/integrations/homeassistant/contextfmt/) —
        a small, domain-owned package whose exports (`Format`,
        `SemanticState`, normalizers, etc.) are the only formatter
        surface providers need to call. New formatters should look
        like that, not be inlined into providers, runtime tools, or
        ad-hoc helpers scattered across the consumer side.
  - [ ] If formatter logic is being reimplemented inline at the
        provider level — or duplicated across domain packages —
        consolidate it into the domain's `contextfmt` package (or
        promote a shared helper if it's genuinely cross-domain).
- [ ] **Documentation audit**
  - [ ] [README.md](../README.md) — accurate description of current capabilities
  - [ ] [docs/understanding/](understanding/) — architecture, philosophy, design docs reflect actual implementation
  - [ ] [docs/operating/](operating/) — getting started, integration, deployment guides reflect current reality
  - [ ] [docs/reference/](reference/) — tools, CLI, API docs accurate
  - [ ] [CONTRIBUTING.md](../CONTRIBUTING.md) — accurate for current development workflow
- [ ] **Config review**
  - [ ] [`examples/config.example.yaml`](../examples/config.example.yaml) regenerated and committed (`go generate ./internal/platform/config/...`)
  - [ ] Stale config-owned tool lists removed where compiled defaults or MCP `default_tags` can own membership
  - [ ] Document roots reviewed intentionally:
    - [ ] `paths.core` for persona/ego/metacognitive and other high-integrity docs
    - [ ] `paths.kb` for curated knowledge
    - [ ] `paths.generated` for model-produced durable artifacts
    - [ ] `paths.scratchpad` for low-integrity writable work
  - [ ] `data_dir` kept separate from document roots
- [ ] **Model and tooling review**
  - [ ] Premium/ops/assist semantics still match operator expectations
  - [ ] Dynamic model-registry overlays reviewed for any temporary canary-only policy changes
  - [ ] MCP server configuration verifies the right tools will land in the intended tags/toolboxes
- [ ] **Code surface hygiene**
  - [ ] New code paths use inherited/component loggers, not ad-hoc `slog.Default()` where request or subsystem context matters

## 2. Build readiness

Tree is clean — verify it builds.

- [ ] `just ci`
- [ ] `just build`
- [ ] `just release-build-snapshot <version>` for at least the primary operator target

## 3. Cut artifacts

Decide the operator path first; the rest follows from that choice.

- [ ] Operator path chosen:
  - [ ] `just release-github <version> [auto|prerelease|release]` for a real published release
  - [ ] `just deploy-macos-pkg user@host` for live-host pkg testing without cutting a release
- [ ] `just release-github <version>` completes on the macOS Tahoe release workstation
- [ ] Prepared release metadata matches the commit being published
- [ ] Release tag created from a clean, up-to-date `main`
- [ ] Local release upload publishes the two macOS `.pkg` files, the two Linux tarballs, and a version-scoped `thane_<version>_checksums.txt` to the GitHub release
- [ ] GitHub release workflow publishes the GHCR multi-arch container image
- [ ] GitHub build provenance attestations are available for the container image
- [ ] GitHub release token is set explicitly (`THANE_GH_TOKEN`) for tag creation and release upload
- [ ] macOS release signing credentials are set (`THANE_CODESIGN_IDENTITY`) if shipping a Developer ID-signed build
- [ ] macOS installer signing credentials are set (`THANE_INSTALLER_IDENTITY`) if shipping signed `.pkg` artifacts
- [ ] macOS notarization profile is set (`THANE_NOTARY_PROFILE`) if shipping a notarized/stapled macOS installer package
- [ ] macOS installer packages inspect cleanly (`pkgutil --check-signature`, distribution metadata, current-user-home install domain, and architecture gating all match the intended release)
- [ ] If a manual breakpoint was needed, `just prepare-release <version>` and `just publish-release <version> [auto|prerelease|release]` were used intentionally instead of by habit

## 4. Post-deploy validation

After the build lands on its target host. Verifies the release in situ
and confirms rollback is real.

- [ ] **Pre-deploy operator hygiene**
  - [ ] Existing production config backed up before swap
  - [ ] Previous binary kept locally on the host so rollback is one move
- [ ] **Live-build sanity**
  - [ ] `/v1/version` reports the intended commit after restart
  - [ ] `11434` exposes only the intended virtual model suite
  - [ ] MCP servers needed in production start successfully and their tools land in the intended tags/toolboxes
- [ ] **Canary smoke tests**
  - [ ] One plain `8080` chat request succeeds
  - [ ] One `11434` `thane:premium` request succeeds
  - [ ] One tool-using request succeeds on the route operators actually use
  - [ ] One Home Assistant request succeeds if HA is production-critical
  - [ ] One scheduler/background loop completes after restart
  - [ ] Dashboard, request viewer, and registry windows load cleanly enough for incident response
  - [ ] If a live host was used for pkg validation, `just deploy-macos-pkg user@host` completed and the target Thane API reported the intended build version after restart
- [ ] **Log review**
  - [ ] Check recent `WARN`/`ERROR` output after restart
  - [ ] Distinguish startup burst noise from steady-state regressions
  - [ ] Remove or explain stale config warnings instead of normalizing them away
  - [ ] Confirm request, loop, tool, and model fields are present on new operational log lines
- [ ] **Rollback readiness**
  - [ ] Restart path uses the real supervisor in production, not vestigial install-time service scripts
  - [ ] Rollback steps are short enough to execute under pressure

## 5. Communication

- [ ] PR description summarizes the user-visible changes and operational implications
- [ ] Known alpha/canary risks are written down explicitly
- [ ] Follow-up cleanup items are split from release-blocking fixes
- [ ] **GitHub release notes drafted on the release object itself**, not in a repo document.

  Thane release notes are narrative. Pattern them after recent releases — [v0.9.1 — The Operational Hardening Release](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.1) is the current canonical example. The notes make a story out of the release and are explicit about the *whys* behind the features. They draw heavily from constituent PR descriptions and issue comments, because that's where the why actually got written down. They are *not* an autogenerated changelog with a marketing intro.

  - [ ] **Title and theme.** Pick a center of gravity and name it: `vX.Y.Z — The {Theme} Release` (e.g. *Operational Hardening*, *Convergence*). State the theme in the title and reinforce it in the opening paragraph. If the release lacks a thematic backbone, that itself is signal — call it a maintenance bump and keep it short rather than inflating it.
  - [ ] **Opening framing paragraph.** One to three sentences placing this release in the larger arc of the project. "v0.9.1 is the first release after the convergence work of v0.9.0; the architecture is no longer trying to become one thing — now it starts getting sharper, safer, and easier to operate as that one thing." sets context that a flat changelog cannot. Lift framing language from the PR descriptions and issue comments where the writers captured *why* in the moment.
  - [ ] **Walk the merged PRs as source material.** Enumerate everything that landed since the previous tag — `git log --merges <prev_tag>..HEAD --oneline` or `gh pr list --base main --state merged --search "merged:>YYYY-MM-DD"`. For each non-trivial PR, read the description and any thoughtful comments. The release notes are a synthesis of that material, not a transcript. Linked issues often carry the *why* better than the PR title; follow those links and pull what's useful.
  - [ ] **Group by operational story, not by code area.** Sections should be themes the operator will recognize (*Document Substrate*, *Anthropic Provider Hardening*, *Tool and Context Assembly*, *Scoped Awareness*) — not *Bug fixes* or *Internal refactors*. A bullet's home is wherever the user-facing implication makes most sense. Each section gets a short bolded lead phrase and a why-driven sentence or two.
  - [ ] **Write each bullet around the why.** *"The tool suite assembles before capability tags are finalized"* is incomplete on its own. *"The tool suite now assembles before capability tags are finalized. `tools.Provider` gives subsystems a common declaration contract, while async runtimes such as Signal can declare tools early and bind handlers later. This fixes the `add_context_entity` missing-from-`awareness` bug and removes a whole class of startup-order drift."* is the bar. The reader should finish each bullet knowing both what changed and why it mattered enough to ship.
  - [ ] **Cite inline.** Put `(#NNN)` at the end of each bullet, not as a separate "PRs included" footer. Use issue numbers when an issue captures the why better than the PR; use the PR number when the PR description carries the narrative; multiple references on one bullet are fine and common.
  - [ ] **Diff stats line.** Include the diff stats for the release range — `git diff --shortstat <prev_tag>..<this_tag>` produces the wording. *"183 files changed, 18,265 insertions, 2,577 deletions."* The number doesn't justify the release on its own, but it gives operators a rough scale anchor.
  - [ ] **Upgrade Notes section** when operators need to do or expect anything — config migration, schema change, behavior shift, deprecated path. Keep it short, concrete, numbered. This is the part operators read most carefully; it earns its space by being practical, not aspirational.
  - [ ] **Known alpha/canary risks** that survived into the release belong here too, named plainly. If a feature is hardened-but-still-evolving, say so — operators trust the notes more when the notes admit limits.
  - [ ] **Footer with full changelog link.** `**Full Changelog**: https://github.com/nugget/thane-ai-agent/compare/<prev_tag>...<this_tag>`.
  - [ ] **Read it cold before publishing.** Notes written in PR-by-PR order while walking the log read that way. Close the source tabs, read the assembled notes start to finish, and ask: does the story hold? If a section is adjacent bullets without a thematic point, it needs an opener — or the bullets need to merge into one. If the framing paragraph promises a theme the bullets don't deliver, fix one of them.
