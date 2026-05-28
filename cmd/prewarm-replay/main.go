package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runtime knobs visible to every subcommand. Defaults match the
// production prewarm config; the operator overrides via flags when
// sweeping thresholds.
type globals struct {
	DataDir    string
	Format     string // "human" or "json"
	MaxResults int    // ArchiveContextProvider max hits
	MaxBytes   int    // ArchiveContextProvider byte budget
	MaxFacts   int    // SubjectContextProvider cap
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "prewarm-replay:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("a subcommand is required")
	}

	g, sub, subArgs, err := parseGlobals(args)
	if err != nil {
		return err
	}

	switch sub {
	case "replay":
		return cmdReplay(g, subArgs)
	case "query":
		return cmdQuery(g, subArgs)
	case "batch":
		return cmdBatch(g, subArgs)
	case "compare":
		return cmdCompare(g, subArgs)
	case "surfaces":
		return cmdSurfaces(g, subArgs)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (try: replay | query | batch | compare)", sub)
	}
}

func parseGlobals(args []string) (*globals, string, []string, error) {
	fs := flag.NewFlagSet("prewarm-replay", flag.ContinueOnError)
	defaultDir := filepath.Join(os.Getenv("HOME"), "Thane", "db")
	dataDir := fs.String("data-dir", defaultDir, "directory containing the prod sqlite DBs")
	format := fs.String("format", "human", "output format: human | json")
	maxResults := fs.Int("max-results", 3, "ArchiveContextProvider max hits per turn")
	maxBytes := fs.Int("max-bytes", 4000, "ArchiveContextProvider byte budget per turn")
	maxFacts := fs.Int("max-facts", 10, "SubjectContextProvider max facts per turn")

	if err := fs.Parse(args); err != nil {
		return nil, "", nil, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return nil, "", nil, fmt.Errorf("a subcommand is required after global flags")
	}
	g := &globals{
		DataDir:    *dataDir,
		Format:     *format,
		MaxResults: *maxResults,
		MaxBytes:   *maxBytes,
		MaxFacts:   *maxFacts,
	}
	return g, rest[0], rest[1:], nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `prewarm-replay — tune the prewarm context providers against prod data.

USAGE
    prewarm-replay [global flags] <subcommand> [subcommand flags]

GLOBAL FLAGS
    -data-dir DIR     directory containing the prod sqlite DBs (default ~/Thane/db)
    -format FORMAT    output format: human | json (default human)
    -max-results N    ArchiveContextProvider hits cap (default 3)
    -max-bytes N      ArchiveContextProvider byte budget (default 4000)
    -max-facts N      SubjectContextProvider facts cap (default 10)

SUBCOMMANDS
    replay   reconstruct a stored conversation's wake and run every provider
    query    synthesize an ad-hoc wake from inline inputs
    batch    aggregate hit-rate / coverage stats over a window of turns

Run "prewarm-replay <subcommand> -h" for subcommand flags.`)
}
