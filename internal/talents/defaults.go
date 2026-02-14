package talents

import "embed"

//go:generate sh -c "rm -rf defaults && mkdir defaults && cp ../../talents/*.md defaults/ && echo '*.md' > defaults/.gitignore"

// DefaultFiles contains the embedded default talent markdown files
// (copied from the repo-root talents/ directory by go:generate).
// Used by thane init to populate a new working directory.
//
//go:embed defaults/*.md
var DefaultFiles embed.FS
