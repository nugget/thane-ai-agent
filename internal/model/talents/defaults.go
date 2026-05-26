package talents

import "embed"

// The `find ... -not -name README.md` excludes the talents/README.md
// authoring guide from the embedded set — operators getting a fresh
// install don't need the contributor docs in their workspace, and the
// README's repo-relative links break when resolved from the embedded
// copy's path depth.
//go:generate sh -c "mkdir -p defaults && rm -f defaults/*.md && find ../../../talents -maxdepth 1 -name '*.md' -not -name README.md -exec cp {} defaults/ \\;"

// DefaultFiles contains the embedded default talent markdown files
// (copied from the repo-root talents/ directory by go:generate).
// Used by thane init to populate a new working directory.
//
//go:embed defaults/*.md
var DefaultFiles embed.FS
