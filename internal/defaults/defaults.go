// Package defaults provides embedded copies of default configuration
// and persona files for the thane init subcommand.
package defaults

import _ "embed"

//go:generate sh -c "cp ../../examples/config.example.yaml . && cp ../../examples/persona.example.md ."

// ConfigYAML is the embedded default configuration file
// (examples/config.example.yaml), written by thane init.
//
//go:embed config.example.yaml
var ConfigYAML []byte

// PersonaMD is the embedded default persona file
// (examples/persona.example.md), written by thane init.
//
//go:embed persona.example.md
var PersonaMD []byte
