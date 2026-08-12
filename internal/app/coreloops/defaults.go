// Package coreloops embeds the loop definition documents Thane ships
// with.
//
// These are the built-in defaults for the core service loops. An
// operator overrides one by placing a document of the same name in the
// core document root, which wins; absent that, the shipped document is
// what boots when config enables the loop. A loop with no document and
// no enabled flag does not run at all. Shipping the defaults rather
// than seeding them at install means an upgrade cannot leave an
// enabled loop silently undefined because a copy step was missed.
package coreloops

import "embed"

//go:generate sh -c "mkdir -p defaults && rm -f defaults/*.md && cp ../../../loops/*.md defaults/"

// Documents holds the embedded loop definition markdown files, copied
// from the repo-root loops/ directory by go:generate. Authoring happens
// there; this mirror is generated, exactly as the talents mirror is.
//
//go:embed defaults/*.md
var Documents embed.FS
