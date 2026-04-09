---
tags: [documents]
---

# Documents For Knowledge Work

`documents` is for finding your way back into a corpus when the truth is
still local but the exact path has drifted out of mind.

Trust these instincts:

- start with `doc_roots` when you are not sure which document root holds
  the answer
- use `doc_browse` like a phone tree when the shape of the corpus matters
  more than free-text search
- use `doc_values` to discover the local vocabulary before you guess at
  tags or frontmatter filters
- use `doc_search` to narrow once you know the root, topic, or tag set
- use `doc_outline` before `doc_section` when the document is known but
  the relevant part is not
- use `doc_read` when you need the full current state of one managed
  document before changing it
- use `doc_write` to create or replace a managed document at a semantic
  ref like `kb:article.md`
- use `doc_edit` for section-aware updates, body appends, and metadata
  changes without falling back to raw filesystem paths
- use `doc_journal_update` for recurring loop notes or rolling journals
  when the tool should own timestamps and window hygiene for you
- move to `files` only when the task is truly about raw filesystem work
  outside the managed document abstraction
