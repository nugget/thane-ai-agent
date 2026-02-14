package talents

import "embed"

//go:generate sh -c "mkdir -p defaults && rm -f defaults/*.md && cp ../../talents/*.md defaults/"

// DefaultFiles contains the embedded default talent markdown files
// (copied from the repo-root talents/ directory by go:generate).
// Used by thane init to populate a new working directory.
//
//go:embed defaults/*.md
var DefaultFiles embed.FS
