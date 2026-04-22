# Milestone Strategy

How we use GitHub milestones to plan and track releases.

## Versioning Philosophy

**v1.0.0 is a meaningful threshold, not an arbitrary number.** It marks the
moment Thane transitions from a single-instance universe to a population.
Pre-1.0 is the single-thane era. At 1.0, a second production thane launches
and establishes mutual trust with the first.

The 1.0 gate is defined by two concrete questions:

1. *Can we spin up a second thane instance and have it cryptographically
   establish mutual trust with the first?*
2. *Can a technically skilled enthusiast install and bootstrap a thane
   instance from docs alone, without our involvement?*

If either answer is no, it's not 1.0.

## Milestone Types

### Major milestones (v1.0.0, v2.0.0)

Semantic thresholds with real criteria. Issues assigned here represent
hard requirements for that threshold. They stay in the major milestone
until they're pulled into an active point release for implementation.

**v1.0.0 criteria:**
- Cryptographic identity established at init (SSH keypair, internal CA)
- Managed document roots with signature-required integrity policy
- Peer trust model: flat by default, hierarchical by choice
- Message bus with signed envelopes for inter-component communication
- Model-guided onboarding for new instances
- Cohesive log storage with primary-source retention
- Documentation and init flow sufficient for an unassisted install by
  a technically skilled enthusiast
- Everything needed for two thane instances to coexist with mutual trust

### Point releases (v0.9.0, v0.9.1, v1.1.0)

Active shipping targets. These are ephemeral and focused — a small batch
of issues cherry-picked from the backlog or from a major milestone.

When planning a point release, pull issues from wherever they make sense:
- From v1.0.0 to chip away at the multi-thane gate
- From the unlabeled backlog for independent improvements
- New work that emerged since the last release

When a point release ships, close the milestone. Open the next one and
pull in the next batch.

### Post-major (v1.1.0, future)

Good ideas that don't gate the next major threshold. These can be pulled
forward into point releases when priorities shift.

## Workflow

1. **Issues start in the major milestone they gate** (if any) or remain
   unassigned if they're independent of a threshold.
2. **To plan a point release**, cherry-pick issues into the new point
   release milestone. If an issue was in v1.0.0, it moves to the point
   release — the v1.0.0 milestone tracks what's *remaining*, not a
   frozen manifest.
3. **Ship the point release**, close the milestone.
4. **Repeat.** When every v1.0.0 issue has shipped through point releases,
   the v1.0.0 milestone is empty and the threshold is met. Tag the
   release as v1.0.0.

## Thematic Organization

Milestones track *when* something ships. Labels track *what area* it
belongs to. An issue's thematic identity lives in its labels (e.g.,
`security`, `architecture`, `loops-ng`), not its milestone.

To see progress on a pillar like identity/trust: filter by label across
all milestones. To see what's shipping next: look at the current point
release milestone.

## Guidelines

- **Fuzzy is fine.** Milestone assignment is a planning tool, not a
  contract. Move issues freely as priorities shift.
- **One milestone per issue.** GitHub enforces this. Use the most
  actionable milestone — usually the release target.
- **Close milestones when shipped.** Don't leave stale milestones open.
- **Major milestones drain to zero.** When all issues have been pulled
  through point releases and shipped, the major milestone is met.
