# CLAUDE.md

For project conventions, build commands, architecture, and contribution
guidelines, see [AGENTS.md](AGENTS.md). Everything below is specific to
the Claude Code operator experience on this repo.

## Commit Signing

The SessionStart hook configures repo-local signing automatically each
session using Claude's dedicated key (see `~/.claude/CLAUDE.md` for
identity details). Verify signing is active before your first commit:

```bash
git config commit.gpgsign   # should return true
```

## CI Gate

**MANDATORY: `just ci` must pass locally before every `git push`. No
exceptions.** Do not rely on GitHub Actions — run the full gate locally
first and fix any issues before pushing. This is a hard requirement.

## Model-Facing Work

When touching tool implementations, tool outputs, schemas, descriptions,
or any context consumed by model loops, read these first and apply their
conventions during the work:

- [docs/model-facing-context.md](docs/model-facing-context.md)
- [docs/model-facing-tools.md](docs/model-facing-tools.md)

## GitHub Collaboration

Be a good GitHub collaborator. Review threads left open signal unfinished
work — always close the loop. Leave PRs clean and reflective of reality.

**When addressing review feedback:**
1. Fix the issue in a commit
2. Reply to the thread with the fixing commit hash and a one-line
   explanation
3. Resolve the conversation
4. If deferring (out of scope, follow-up issue), say so explicitly before
   resolving

**After a round of fixes:** Request re-review so the reviewer knows the
ball is back in their court.

**Resolving threads via CLI:**
```bash
gh api graphql -f query='mutation { resolveReviewThread(input: {threadId: "THREAD_ID"}) { thread { isResolved } } }'
```

**PR hygiene:**
- Check off test plan items as they are verified
- Use `Refs #NNN` or `Closes #NNN` in commit bodies
- Keep the PR description accurate as scope evolves
