// Package defaulttalents provides embedded copies of the shipped talent
// files for use by the init subcommand. This package exists solely to
// satisfy go:embed's requirement that embedded files reside in or below
// the embedding package directory.
//
// The runtime talent loader lives in internal/talents.
package defaulttalents

import "embed"

// FS contains the shipped talent markdown files.
//
//go:embed *.md
var FS embed.FS
