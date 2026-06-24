//go:build !uiharness

// This package is the UI harness (see main.go, //go:build uiharness). Without
// that tag it builds to an empty main so `go build ./...` and the CI gate stay
// green without compiling the harness.
package main

func main() {}
