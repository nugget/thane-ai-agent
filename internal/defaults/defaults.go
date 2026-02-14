// Package defaults provides embedded copies of example files for the
// init subcommand. Files are copied from the repo-root examples/
// directory at build time via go:generate.
package defaults

import _ "embed"

// ConfigYAML is the example configuration file.
//
//go:generate sh -c "cp ../../examples/config.example.yaml . && cp ../../examples/persona.example.md ."
//go:embed config.example.yaml
var ConfigYAML []byte

// PersonaMD is the example persona file.
//
//go:embed persona.example.md
var PersonaMD []byte
