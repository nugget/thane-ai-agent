# Document Roots

When Thane works well, your documents feel easy to refer to and easy to
find again.

That starts with **document roots**: named directories in config that
tell Thane which local collections matter.

```yaml
roots:
  kb: ./knowledge
  scratchpad: ./scratchpad
  dossiers: ~/Vaults/private-dossiers
```

> The legacy `paths:` / `doc_roots:` split is still parsed with a
> deprecation warning but cannot be combined with `roots:`. Prefer
> `roots:`, which keeps a root's path and its policy in one entry.

Each entry gives a directory a stable identity. Instead of treating your
files as one anonymous pile, Thane can understand that some notes live
in a knowledge base, some are scratch work, and some belong to a more
private long-form collection.

## Why This Helps

The point is not to make you think about implementation details. The
point is to make everyday use feel smoother.

Good document roots help when:

- you want Thane to keep track of a body of notes over time
- you have several different kinds of material and want them kept
  distinct
- you create something today and want to be able to find it again later
- you want to refer to a collection by name instead of by fragile paths

If you give a directory a stable root, Thane has a better chance of
finding the right thing without you needing to remember the exact file
location yourself.

## What To Put In A Root

A document root should be a coherent collection, not just a convenient
folder.

Good examples:

- `kb:` for durable reference material
- `scratchpad:` for rough working notes
- `generated:` for reports and other machine-produced outputs
- `dossiers:` for long-form background material on people, projects, or
  places
- `research:` for a project-specific note collection

Less good examples:

- a giant home directory with unrelated files mixed together
- a temporary folder that changes shape constantly
- a directory whose contents you do not actually want Thane treating as
  part of its working world

The cleaner the boundary, the easier it is for Thane to stay oriented.

## Adding Your Own Roots

You do not need any special indexing section or separate feature flag.

If a directory is listed under `roots:` and exists on disk, it becomes
one of Thane's managed local document collections.

Example:

```yaml
roots:
  kb: ~/Thane/knowledge
  scratchpad: ~/Thane/scratchpad
  dossiers: ~/Vaults/private-dossiers
  research: ~/Work/research-notes
```

With a setup like this, you can gradually build several stable document
collections without changing code or teaching Thane a new subsystem each
time.

## Root Policy

Most roots do not need extra policy. If a root is listed under `roots:`
as a bare string and exists on disk, Thane indexes markdown in that root
and managed document tools may write it.

When a root needs a stronger contract, give its entry the full mapping
form with policy fields:

```yaml
roots:
  kb:
    path: ~/Thane/knowledge
    authoring: managed
    git:
      enabled: true
      sign_commits: true
      verify_signatures: warn
      signing_key: ~/.ssh/id_ed25519
  scratchpad:
    path: ~/Thane/scratchpad
    indexing: false
    authoring: managed
```

Policy is deliberately attached to the root, not to individual tools or
prompts. A loop-declared output, a direct document write, and the
corpus-aware intake flow should all meet the same root contract.

Source checkouts can also be named roots, even though they are not document
corpora. `forge_repo_follow` registers them automatically when given a
handle such as `repo_root: thanecode`; Go chooses the physical checkout
location beneath `workspace.path`. Raw file tools then resolve the prefix:
`file_read` can read `thanecode:go.mod`, while `file_tree`, `file_search`, and
`file_grep` can traverse the checkout. Repository roots are always read-only
to these tools and never enter the `doc_*` index.

Trust has a deliberately different meaning here. Document roots may assert
signed history and reject unattested worktree bytes. A repository mirror
instead asserts its remote, branch, synced commit, and sync age. The forge
context carries those facts; document signature verification is not applied
to the mirror.

The current policy fields are:

- `indexing`: set `false` when a root may be written/read by exact ref
  but should stay out of browse/search results.
- `authoring`: `managed` allows document mutations, `read_only` blocks
  them, and `restricted` reserves the root for narrower future flows.
- `git.enabled`: records that the root participates in git-backed
  provenance.
- `git.sign_commits`: signs and commits each managed write/delete.
- `git.verify_signatures`: sets the consumer policy: `none`, `warn`,
  or `required`.
- `context.advertise`: controls bounded document offers: `never`,
  capability-`tagged`, `always` ambient, or `exact_subject`. The last mode
  offers a document only when one of its tags exactly matches a canonical
  request subject; it has no lexical or ambient fallback.

### Signed roots

Signature verification reads the repository-local `.allowed_signers`
file, with the root's declared `seed_signers` standing behind it as a
permanent floor. Thane creates the file once, when a signed root is
first established, from the agent key plus that root's declared
`seed_signers`:

```yaml
roots:
  knowledge:
    path: ./knowledge
    seed_signers:
      - principal: alice@example.com
        key: "ssh-ed25519 AAAA..."
        label: "Alice laptop"
    git:
      enabled: true
      sign_commits: true
      signing_key: ~/.ssh/id_ed25519
```

After that the file is the root's own trust surface and config never
rewrites it. Adding signers is done by editing and committing
`.allowed_signers` — a change that must be signed by one of the root's
seed signers.

The in-tree file does not have the last word on seeds, though. A
declared seed signer remains trusted for ordinary verification even when
`.allowed_signers` stops listing it: when the in-tree file does not
vouch for a commit, verification falls back to the declared seed set.
That floor is what lets a seed key repair a polluted trust file — without
it, the one key entitled to fix `.allowed_signers` would be refused by
the very file it needs to fix. Every commit trusted this way is logged
as a WARN naming both remedies: restore the entry in `.allowed_signers`,
or drop the key from `seed_signers` if withdrawing it was intended. The
flip side is that editing `.allowed_signers` alone never revokes a seed
signer; only removing it from config does.

`seed_signers` is declared per root rather than once for the instance,
because roots have different trust domains. The keys entitled to sign a
corpus synced from a remote should not automatically be entitled to sign
the config that decides what the whole system trusts. A root that signs
its commits must declare seed signers, since signed history nobody
decided to admit is a signature without a claim behind it.

### What admission checks

Asking only "is this commit signed by a key in `.allowed_signers`?" lets
a repository vouch for itself, because whoever wrote that file also chose
what it says. Admission is the question that comes first, and it is the
one question a root cannot answer in its own favor — seed signers live in
config, outside the repository they govern.

At boot, every git-backed root that declares seed signers and sets
`verify_signatures` to `warn` or `required` must satisfy three rules:

1. **One birth.** The root has exactly one parentless commit. A history
   grafted from two independent roots is refused; admitting it because
   one of those births checks out would let the other in through the side
   door.
2. **An attributable birth.** That commit is signed by a declared seed
   signer.
3. **A trust file only seeds have touched.** Every commit that created or
   changed `.allowed_signers` is signed by a declared seed signer.

Only seed signers satisfy these three rules. The root's own
`.allowed_signers` gets no say in them — if it did, a commit that added a
key could be validated by the very entry it introduced.

Keys the trust file delegates to may sign ordinary content. Only a seed
signer may widen the trust file itself, so a delegated key cannot
delegate further. That is deliberately stricter than the general case: it
forbids one collaborator admitting the next. The reason is soundness
rather than convenience. "Signed by a key trusted at the time" only means
something relative to a commit's own ancestry, and roots that sync
bidirectionally can merge — so a permissive walk that ordered commits by
date would judge a key added on a side branch as trusted earlier than it
really was. The strict rule is order-independent, which makes it correct
on every history shape rather than only the linear ones. Transitive
delegation can arrive later as ancestry-aware composition, with this rule
as its degenerate case.

The pleasant side effect is that admission is cheap. With no chain to
follow, the cost is one signature check for the birth plus one per
trust-file change — on a corpus of several thousand commits that is
typically a handful of checks rather than thousands.

### Declaring the agent

A root Thane creates is born signed by the agent's own key, so an
agent-founded root is admitted only if it declares the agent:

```yaml
    seed_signers:
      - principal: thane@provenance.local
        key: "ssh-ed25519 AAAA..."   # public half of git.signing_key
        label: "Self"
```

Omitting it is how a root says its own agent may not establish or amend
it. That is the intended shape for `core`: an operator founds it, the
agent writes content into it, and the agent cannot rewrite the trust
surface of the root holding the config that decides what the instance
trusts.

Be precise about what that buys. Where the agent has shell access it can
still *write* config; what it cannot do is produce a valid signature for
the change, so the boot gate refuses and names the failing check. This is
detection, not prevention — and detection is the property worth having,
because the realistic failure is not a deliberate adversary but an agent
steered by a poisoned document into "helpfully" relaxing a check. That
drift is silent by nature. A refusal at boot makes it loud.

Because a birth is the one thing no later commit can repair, Thane
refuses to *create* a root whose seed signers omit the agent — while the
repository is still empty and the remedy is a config line rather than a
history rewrite.

### When admission fails

A failed root reports through the same `verify_signatures` policy as
every other check: `required` refuses to start, `warn` logs and
continues. The message names the check and, in the common case, who
actually signed:

```
doc_roots.kb admission boot verification: the root commit 7c939f595ac9
of /srv/thane/knowledge is not signed by a declared seed signer, so this
root's birth is unattributed: it was signed by thane@provenance.local,
the agent's own key — declare that principal in this root's seed_signers
if the agent is entitled to establish it, or re-establish the root with a
commit signed by a declared seed
```

The usual cause is a root founded before its seed set was declared, or
one founded by the agent at an instance that never declared the agent.
Both are config fixes. Rewriting history to install a different founder
is the alternative, and for a corpus behind a bidirectional remote it is
almost never the right one — declaring who actually founded the root is
the honest record.

You do not have to discover this by watching a deploy fail. `thane
validate` reports admission for every root, and exits non-zero when a
`required` root would be refused, so `thane validate && thane serve`
remains a real guard:

```
✓ Root admission: core, kb, projects
```

Roots under `warn` are listed with a `!` rather than a `✗`, because serve
logs those and starts anyway; validate does not invent strictness the
gate does not have. Validate never creates anything, so a signing root
whose directory does not exist yet is simply absent from the report —
serve would create and birth-commit it, and there is no history to judge
until it does.

### Read-side enforcement

Signature-required roots are the place for high-integrity authored
knowledge, such as owner-tagged knowledge articles. When verification
is `required`, Thane blocks the following content paths when the
target content is not cleanly covered by trusted signed git history:

- Document store reads (`Read`, indexed browse and search surfaces)
- Loop-declared output context
- Tagged context articles
- The model's `file_read` tool when the resolved path lies inside a
  managed root
- Inject-files each time they are read into the prompt, with startup
  fail-fast verification for initially configured files
- Startup-time talents, loaded only after their source markdown files
  pass verification

When verification is `warn`, Thane records and logs verification
failures but still lets the content load.

The raw `file_write` and `file_edit` tools are stricter than read
verification. They cannot mutate read-only/restricted roots, and they
cannot mutate roots with signed git provenance; those changes must go
through managed document tools so root writers can preserve authoring
policy, git history, and signatures.

### Concurrent writers

Git history makes a mistaken overwrite recoverable, but history alone does
not stop a stale loop from replacing another writer's current work. On writable
git-backed roots, a managed read therefore records the current file revision as
a hidden receipt keyed by loop/conversation and document ref. The model never
passes hashes between tools. A later `doc_write`, `doc_edit`, or
`doc_journal_update` supplies that receipt to the root writer internally.
Read-only history roots do not create mutation receipts.

The root writer compares and commits managed mutations under the same lock, and
the Git ref update also verifies its expected parent. A stale receipt returns
`applied: false`, leaves the worktree and HEAD unchanged, and provides a bounded
base-to-current patch. The receipt advances to current HEAD so the caller can
reconcile and retry without another hash-bearing parameter. A missing or
oversized patch falls back to a bounded current excerpt. A scoped whole-body
replacement of an existing document requires a prior managed read; structured
edits and creates derive a safe current/absent base within the operation.

This coordinates Thane's managed writers and rejects operator edits that are
already dirty when the mutation begins. It is not a general filesystem lock:
an editor that ignores this coordination can still save in the narrow window
between the final worktree check and replacement. Git history remains the
recovery boundary for that external race.

Directory-walk surfaces (`file_list`, `file_tree`, `file_stat`,
`file_search`, `file_grep`) intentionally do not consult the verifier.
`file_list`, `file_tree`, `file_stat`, and `file_search` return only
paths and metadata. **`file_grep` is different**: it returns short
content excerpts that are
*not* verified, so under `verify_signatures: required` it can surface
snippets from files that would be blocked by `file_read`. If a result
matters to you, re-read it through `file_read` — that path is gated
and will fail if the underlying content is not covered by trusted
signed history.

## A Few Practical Guidelines

- Prefer a small number of well-named roots over dozens of tiny ones.
- Keep each root internally coherent.
- Markdown is the best-supported format today.
- If a collection matters enough that you want Thane to reuse it later,
  give it a root instead of leaving it buried in a generic folder.
- If a root is very high integrity or operationally sensitive, be
  deliberate about how you want it managed and edited.

## Corpus-Aware Intake

When Thane is about to create durable knowledge, the default path is
`doc_create`, which runs the intake analysis and writes in one call
when placement is clean — no filename guessing, and the collision check
rides the create verb itself. The two-step `doc_intake` → `doc_commit`
form remains for inspecting the plan first. Intake looks at the target
root, searches related documents, checks observed tag vocabulary and
path patterns, and returns a proposed destination with a recommended
action:

- `create_new` when the corpus does not appear to contain the idea yet
- `update_existing` when a related document looks like the better home
- `append_existing` when the request sounds like a journal or running
  note
- `draft_for_review` when root policy or ambiguity makes a write
  inappropriate

The result includes an `intake_id`, normalized title/tags/frontmatter,
related documents with similarity scores, and a `commit_plan`. The
model then calls `doc_commit` with that `intake_id` and the approved
body. If intake reports a caution, such as a high-overlap existing
document, `doc_commit` requires `confirm=true` before writing.

This keeps taxonomy decisions anchored in the existing corpus. The raw
mutation tools still exist for deliberate exact-path work, but intake is
the normal authoring flow for new knowledge.

## Generated Documents

Generated markdown should be legible to both people and the document
index. When Thane intentionally writes an artifact into a managed root,
the file should include document-local provenance in frontmatter:

```yaml
generated_by: "media_save_analysis"
generated_at: "2026-04-26T18:14:15Z"
document_kind: "media_analysis"
refresh_strategy: "immutable"
source_refs:
  - "url:https://example.test/watch?v=abc123"
  - "feed:security-news"
managed_root: "generated"
```

These fields answer a different question than git history. Frontmatter
tells Thane what kind of generated artifact it is, what source material
it came from, and how future refreshes should treat it. Root-level git
history and signature policy can still answer who changed a file and
whether that change is trusted.

Use `source_refs` for compact, typed references such as URLs,
conversation IDs, Home Assistant entity IDs, attachment hashes, or feed
IDs. Use `refresh_strategy` to distinguish one-shot immutable artifacts
from generated files that are replaced, appended to, or maintained as a
rolling window.

## Loop-Declared Outputs

Autonomous and background loops can declare the documents they are
responsible for maintaining. Thane turns those declarations into narrow
runtime tools:

- `replace_output_<name>` for a maintained document that should be
  rewritten as a complete current state.
- `replace_output_<name>` for a working-notes document, which holds the
  loop's current thinking and is rewritten rather than accumulated.
- `publish_output_<name>` for a maintained document that declares
  `facets`: one typed argument per published projection
  (`status_line`, `teaser`, `digest`) plus the full body, written
  together in a single call. Thane renders the document sections from
  the payload, so the loop supplies content and never structure. Declare
  only the projections the document's readers need; `status_line` and
  `teaser` are two shapes of the same outward-facing signal role.

The loop sees a matching context block with the current document content
or recent journal tail, so the document itself remains the durable source
of truth. The generated tools still write through document roots. That
keeps path resolution, indexing, provenance, and root-level integrity
policy in one subsystem.

## Special Case: the Derived Roots

Three root names are reserved: `core`, `self`, and `contacts`. Their paths come
from the workspace, and none takes a path from `roots:`. You may name them
there to set policy; a path given alongside policy is ignored. `core` and
`self` are always registered. `contacts` is opt-in and is registered only when
its policy is declared.

`core` and `self` are separate roots because they answer to different
authorities, and that difference is the whole point of their split.

`core` is what an operator declares Thane to be, and what constrains it:
axioms, persona, mission, the runtime config, talents, and loop definitions.
Nothing an agent writes belongs in it. That is what lets an install narrow
`core`'s signers to the operator alone and set `authoring: read_only` on it —
the posture we recommend, and the only way to shield `config.yaml` from
agent editing. It is also what makes `core`'s HEAD worth reading as evidence:
it moves when the operator moves it.

`self` is what Thane makes of that — its self-concept, its running
observations, its memory of its own work. The core service loops maintain it:
`self:metacognitive.md`, `self:archivist.md`, and `self:ego.md`. It wants the
same integrity treatment as `core` — signed history, admission, verification —
under its own signer set, because the agent is its author.

`self:ego.md` is the case worth understanding, because it is injected into the
interactive agent's system prompt beside axioms, persona, and mission, and the
colocation makes it read as though it shared their authority. It does not.
Those three are what the operator declares Thane to be; `ego.md` is what Thane
has made of that. They are read together because that is where the reflection
is useful, and they are written under different signer policies because they
answer to different authorities.

That distinction is why the injection had to follow the document rather than
stay pointed at `core`: an ego loop writing to one root while the prompt reads
another leaves the agent composing self-reflection it never reads back.

Both are always registered because the core service loops write `self` on
every install. A root an operator had to remember to declare would leave a
fresh install with nowhere for those documents to land.

### Contact dossiers

`contacts` is the optional longitudinal document side of the structured
contact directory. Its canonical document is
`contacts:<canonical-contact-uuid>.md`; using the UUID rather than a name keeps
the dossier stable across renames. Every dossier must carry the matching
`contact:<uuid>` as its only frontmatter tag and the complete status-line /
teaser / digest / full facet ladder. The single-tag rule prevents a broader
subject such as `household` from advertising a private dossier. Go enforces
that shape before any managed write reaches the worktree or Git history.

The boundary is strict: SQLite contacts remain authoritative for UUIDs,
structured identity, communication properties, trust zones, Home Assistant
bindings, and companion attribution. Dossier prose is relationship synthesis,
not a way to mutate those fields. `contact_lookup` and `contact_owner` expose a
bounded dossier ref; `doc_read` opens it when the prose is useful.
`contact_dossier_write` is the canonical mutation door on a managed-writable
root. It accepts the contact UUID plus status-line, teaser, digest, and full
content, then derives the ref, exact private tag, frontmatter, and section
layout in Go. Projection budgets are advertised in the schema, and one failed
call reports every independently correctable projection violation together.
The generic document tools remain available for operator recovery, but models
should not author dossier structure through them.

Fresh `thane init` workspaces declare and establish the root with the agent's
signing key, required signature verification, and this context policy:

```yaml
roots:
  contacts:
    authoring: managed
    context:
      advertise: exact_subject
    seed_signers:
      - principal: thane@provenance.local
        key: "ssh-ed25519 AAAA..." # core/identity/signing_ed25519.pub
        label: agent
    git:
      enabled: true
      sign_commits: true
      verify_signatures: required
      signing_key: core:identity/signing_ed25519
```

`exact_subject` is the privacy and attention boundary: a request carrying
`contact:<uuid>` may receive that contact's compact dossier projection, while
unrelated dossiers get neither ambient nor loose lexical offers. Existing
workspaces can add the same declaration and re-run `thane init` to establish
the signed sibling repository before deployment.

## The Human-Level Rule

If you find yourself thinking:

- “this directory is part of Thane's long-term world”
- “I want to be able to refer to this collection by name”
- “I do not want this to get lost just because the exact path slips my
  mind”

then it probably wants to be a document root.
