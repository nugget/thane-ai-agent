package talents

import "embed"

// DefaultFiles contains the shipped talent markdown files, copied from
// the repo-root talents/ directory at build time via go:generate.
// The defaults/ subdirectory is .gitignored — its contents are build
// artifacts, not source.
//
//go:generate sh -c "cp ../../talents/*.md defaults/"
//go:embed defaults/*.md
var DefaultFiles embed.FS
