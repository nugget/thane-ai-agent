package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "model-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("a subcommand is required")
	}
	switch args[0] {
	case "snapshot":
		return cmdSnapshot(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (try snapshot or run)", args[0])
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `model-eval — replay production model decisions without executing tools.

USAGE
    model-eval snapshot [flags]
    model-eval run [flags]

SUBCOMMANDS
    snapshot   export recent retained model calls to a private 0600 artifact
    run        replay a snapshot against one OpenAI-compatible model

Snapshots contain production prompts, messages, and tool results. They are
written outside Git worktrees by default and must be treated as sensitive.`)
}
