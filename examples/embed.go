// Package examples provides embedded copies of the example config and
// persona files for use by the init subcommand.
package examples

import _ "embed"

// ConfigYAML is the example configuration file.
//
//go:embed config.example.yaml
var ConfigYAML []byte

// PersonaMD is the example persona file.
//
//go:embed persona.example.md
var PersonaMD []byte
