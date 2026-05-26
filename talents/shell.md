---
name: shell
tags: [shell]
kind: trailhead
teaser: "Open only when arbitrary command execution is genuinely the next move — almost everything has a more specific tool."
---

# Shell

`exec` is the broadest, most powerful, most dangerous tool in the
catalog. It runs arbitrary commands through `sh -c`. The deny list
catches the most obvious foot-guns; everything else is your
judgment.

The leaf is small because the right answer is usually "use a more
specific tool." When `exec` *is* the right answer, the safety doctrine
matters more than the syntax.

## Prefer the specific tool — exec is the escape hatch

Most reach-for-shell impulses have a more focused tool that does the
job with structured results and tighter safety:

| You're tempted to run... | Use instead |
|---|---|
| `ls`, `cat`, `find`, `grep` inside the workspace | `files` tag (`file_read`, `file_list`, `file_grep`, `file_search`, `file_tree`) |
| `git status`, `git log`, `gh pr view`, anything PR/issue-shaped | `forge` tag (`forge_pr_get`, `forge_issue_list`, etc.) |
| `curl https://...`, `wget` | `web` tag (`web_fetch`) |
| `ha-cli`, `hass` invocations | `ha` tag (`ha_get_state`, `ha_call_service`, `ha_control_device`) |
| Querying logs | `logs_query` (core; always available) |

`exec` is the right call when *nothing* in the catalog covers the
work: reading outside the workspace sandbox, running a build, poking
at the host OS, checking system services. When you can describe the
work as "run X command," ask first whether there's a focused tool;
when the answer is genuinely no, use `exec`.

## Safety constants

- **`exec` may be disabled at this site.** Shell execution is
  off-by-default; operators opt in. If a call returns "shell
  execution is disabled," that's host policy, not a recoverable
  error — pick a different approach. Don't ask the user to "enable
  shell" unless they brought it up first.
- **The deny list is a backstop, not a sandbox.** `rm -rf /`,
  `mkfs`, `dd if=`, fork bombs and a few other obvious patterns are
  blocked. Everything else runs. Treat `exec` like you would handing
  the command to a root shell on the host — because functionally
  that's what you're doing.
- **No interactivity.** Commands run without a TTY. Anything that
  prompts (apt without `-y`, ssh first-connection acceptance, sudo
  password prompts) hangs until the timeout. Pass non-interactive
  flags; redirect input from `/dev/null` when in doubt.
- **Timeouts are short.** 30 seconds default; 5 minutes maximum.
  Long-running builds, installs, or operations *cannot* run inline.
  See "Delegate long work" below.
- **Output is truncated at 100KB** (each of stdout/stderr separately).
  Filter at the source — pipe through `head`, `tail`, `grep`, `awk` —
  rather than letting truncation eat the signal.
- **Exit codes live in the result, not in the error.** A failing
  command returns a normal result with non-zero `ExitCode`. Always
  check it before trusting `Stdout`; partial output from a failed
  command is a common surprise.

## The diagnose-before-mutate pattern

Anything that mutates state — `rm`, `mv`, `git reset --hard`,
`systemctl restart`, package installs, schema changes — deserves a
dry-run or read-only confirmation pass first:

```json
{
  "command": "ls -la /tmp/staging | head -20",
  "timeout": 10
}
```

…before…

```json
{
  "command": "rm -rf /tmp/staging/old-*",
  "timeout": 30
}
```

The cost of the extra read is one tool call. The cost of mutating
the wrong path is real and usually unrecoverable from this surface.

## Quoting is your responsibility

`exec` runs `sh -c <command>`, so shell metacharacters interpret as
usual. Single-quote anything you want passed literally:

```json
{
  "command": "find /var/log -name '*.gz' -mtime +7"
}
```

The single quotes around `*.gz` keep the shell from expanding the
glob before `find` sees it. Same for substrings in `grep` patterns,
sed expressions, awk scripts. When in doubt, single-quote.

## Delegate long work

Builds, large installs, long-running migrations, anything that won't
finish in 5 minutes — `exec` is the wrong shape. Spawn a delegate
with `thane_assign` and a tag set that includes `shell`:

```json
{
  "task": "Run the project's full CI gate (just ci) and report the failing tests, if any.",
  "tags": ["shell", "files"]
}
```

The delegate runs `exec` inside its own iteration loop with its own
timeout budget. The parent loop stays responsive and the long shell
work doesn't burn the parent's iteration count.

## Reading outside the workspace sandbox

`file_read` is sandboxed to configured workspace roots. When the data
you need is genuinely outside (a system log, a temp file written by
a subprocess, a config in `/etc`), `exec` is the right path:

```json
{
  "command": "cat /var/log/system.log | tail -100"
}
```

This is the only pattern where reaching for `exec` over `file_read`
isn't a smell — `file_read` *cannot* serve this case, so `exec` is
the actual fit.

## Cross-references

- For workspace file work, bounce to `files` — almost every "read /
  list / grep / find inside the workspace" call should land there
  instead.
- For long-running shell work that won't fit in a 5-minute window,
  bounce to `loops_examples` — `thane_assign` with `tags: ["shell"]`
  is the canonical pattern.
- For "I ran a command; the system event side of what happened around
  that time," `logs_query` is the right surface for forensic detail
  beyond the `exec` result itself.
