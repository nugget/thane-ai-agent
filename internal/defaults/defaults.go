// Package defaults provides embedded copies of default configuration
// and persona files for the thane init subcommand.
package defaults

import _ "embed"

//go:generate sh -c "cp ../../examples/config.example.yaml . && cp ../../examples/persona.example.md ."

//go:embed config.example.yaml
var ConfigYAML []byte

//go:embed persona.example.md
var PersonaMD []byte
