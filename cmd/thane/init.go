package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
)

//go:generate sh -c "cp ../../examples/config.example.yaml . && cp ../../examples/persona.example.md ."

// configExampleYAML is the embedded default configuration file
// (examples/config.example.yaml), written by thane init.
//
//go:embed config.example.yaml
var configExampleYAML []byte

// personaExampleMD is the embedded default persona file
// (examples/persona.example.md), written by thane init.
//
//go:embed persona.example.md
var personaExampleMD []byte

// archiveReadmeMD orients a fresh agent or operator landing in the
// archive/ directory — the three top-level subtrees, the invariants,
// and where to look for what.
//
//go:embed archive_readme.md
var archiveReadmeMD []byte

// sourcesThaneReadmeMD documents the current-era thane primary-source
// datasets — their on-disk layout, schema, era, and interpretation
// notes for future archaeologists.
//
//go:embed sources_thane_readme.md
var sourcesThaneReadmeMD []byte

// interactionsSchemaV1JSON is the placeholder JSON Schema for the
// interactions/ record shape (#938 will populate it for real).
//
//go:embed interactions_schema_v1.json
var interactionsSchemaV1JSON []byte

// initOptions carries the operator's answer to "who founds this instance".
type initOptions struct {
	// OperatorKey is an explicit private key path. Empty means discover one
	// from the operator's git configuration.
	OperatorKey string
	// OperatorPrincipal overrides the identity the key signs as.
	OperatorPrincipal string
	// SelfSigned skips the search entirely and founds core with the agent's
	// own key, for an operator who has decided that is what they want.
	SelfSigned bool
}

// resolveOperatorSigner decides which key founds core, and explains the
// choice.
//
// An explicit key is an instruction rather than a preference, so failing to
// load one is an error: silently falling back to a self-signed core after
// being handed a key would produce a weaker instance than the operator asked
// for, and say nothing about it.
func resolveOperatorSigner(ctx context.Context, opts initOptions) (*identity.OperatorSigner, string, error) {
	if opts.SelfSigned {
		return nil, "asked for a self-signed core", nil
	}
	if strings.TrimSpace(opts.OperatorKey) != "" {
		signer, err := identity.LoadOperatorSigner(ctx, opts.OperatorKey, opts.OperatorPrincipal)
		if err != nil {
			return nil, "", fmt.Errorf("operator key: %w", err)
		}
		return signer, "", nil
	}
	signer, why := identity.DiscoverOperatorSigner(ctx, opts.OperatorPrincipal)
	return signer, why, nil
}

// describeCorePosture tells the operator which of the two shapes they got, in
// the vocabulary they already have from TLS.
//
// A self-signed core is not a failure and is not presented as one — it works,
// and for a single-operator instance it may be all anyone wants. What it must
// not do is arrive silently, because the property it gives up is the one an
// operator would assume they had: that the agent cannot re-establish the root
// holding its own config.
func describeCorePosture(w io.Writer, result *identity.BootstrapResult, why string) {
	if !result.SelfSigned {
		fmt.Fprintf(w, "  ✓ core anchored to %s — the agent can write to core but cannot re-establish it\n", result.OperatorPrincipal)
		return
	}
	fmt.Fprintln(w, "  ! core is self-signed: its only seed signer is this instance's own agent key,")
	fmt.Fprintln(w, "    so nothing outside the instance attests to it and the agent could re-establish")
	fmt.Fprintln(w, "    the root that holds its config.")
	if why != "" {
		fmt.Fprintf(w, "    Why: %s.\n", why)
	}
	fmt.Fprintln(w, "    To anchor it to a key you hold, re-initialize an empty workspace with:")
	fmt.Fprintln(w, "      thane init -operator-key ~/.ssh/id_ed25519 <dir>")
}

// runInit initializes a Thane workspace: the directory skeleton, the
// core trust root with its signed birth commit (config, identity
// material, talents), the archive skeleton, and reference copies of the
// example config and persona. Existing files are never overwritten.
func runInit(w io.Writer, dir string, opts initOptions) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	fmt.Fprintf(w, "Initializing Thane workspace in %s\n", absDir)

	// Create directory structure. Talents are not among these: they live
	// inside core now, and arrive as part of its birth commit.
	for _, sub := range []string{"db"} {
		p := filepath.Join(absDir, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}

	// Reference copies at the workspace root, named *.example.* because
	// the runtime never reads them: config loads from core/config.yaml
	// (part of core's birth commit) and persona from core/persona.md.
	// They exist so a fresh workspace carries the annotated examples the
	// operator extends those files from.
	if err := writeIfMissing(w, filepath.Join(absDir, "config.example.yaml"), configExampleYAML, 0o644); err != nil {
		return err
	}
	if err := writeIfMissing(w, filepath.Join(absDir, "persona.example.md"), personaExampleMD, 0o644); err != nil {
		return err
	}

	bundledTalents, err := bundledTalentFiles()
	if err != nil {
		return err
	}

	ctx := context.Background()
	operator, why, err := resolveOperatorSigner(ctx, opts)
	if err != nil {
		return err
	}
	result, err := identity.BootstrapCore(ctx, filepath.Join(absDir, "core"), filepath.Base(absDir), operator, bundledTalents, slog.Default())
	if err != nil {
		return fmt.Errorf("bootstrap core identity: %w", err)
	}
	if result.Created {
		fmt.Fprintf(w, "  ✓ %s (core identity, signing %s)\n", result.CoreDir, result.SigningKeyFingerprint)
		fmt.Fprintf(w, "  ✓ %d talents, signed as part of core's birth\n", len(bundledTalents))
		describeCorePosture(w, result, why)
	} else {
		fmt.Fprintf(w, "  · %s (core identity exists, skipping)\n", result.CoreDir)
	}

	if err := bootstrapArchive(w, absDir); err != nil {
		return fmt.Errorf("bootstrap archive: %w", err)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runtime config is core/config.yaml — add your settings there and commit;")
	fmt.Fprintln(w, "thane serve refuses an uncommitted config. Author core/persona.md the same")
	fmt.Fprintln(w, "way. config.example.yaml and persona.example.md at the workspace root are")
	fmt.Fprintln(w, "annotated references the runtime never reads.")
	fmt.Fprintln(w, "See docs/operating/getting-started.md for guidance on persona vs talents.")
	return nil
}

// bootstrapArchive creates the archive directory skeleton — the
// interactions/, sources/, and meta/ subtrees described in
// archive/README.md — and writes the top-level orientation README,
// the current-era source-of-truth README, and a placeholder schema
// for the future interactions corpus. Idempotent: existing files
// are left untouched.
func bootstrapArchive(w io.Writer, absDir string) error {
	archiveDir := filepath.Join(absDir, "archive")
	for _, sub := range []string{
		"interactions",
		filepath.Join("sources", "thane"),
		filepath.Join("meta", "schema"),
	} {
		p := filepath.Join(archiveDir, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create archive/%s: %w", sub, err)
		}
	}

	files := []struct {
		path string
		data []byte
	}{
		{filepath.Join(archiveDir, "README.md"), archiveReadmeMD},
		{filepath.Join(archiveDir, "sources", "thane", "README.md"), sourcesThaneReadmeMD},
		{filepath.Join(archiveDir, "meta", "schema", "interactions.v1.json"), interactionsSchemaV1JSON},
	}
	for _, f := range files {
		if err := writeIfMissing(w, f.path, f.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeIfMissing atomically creates path with the given permissions and writes
// data to it. If the file already exists, it is left untouched. The create
// uses O_CREATE|O_EXCL so there is no race between checking and writing.
func writeIfMissing(w io.Writer, path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(w, "  · %s (exists, skipping)\n", path)
			return nil
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", path, closeErr)
	}
	fmt.Fprintf(w, "  ✓ %s\n", path)
	return nil
}

// runInitCommand parses `thane init`'s own flags.
//
// The flags exist as much for wrapper installers as for people: a GUI front
// end can ask "which key is yours?" in whatever way suits it and pass the
// answer here, instead of asking someone at first run to reason about trust
// roots.
func runInitCommand(stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	// Parse failures and usage are terminal, and run() puts terminal output on
	// stderr. Sending them to stdout would interleave them with the progress
	// report a caller may be parsing.
	fs.SetOutput(stderr)
	var opts initOptions
	fs.StringVar(&opts.OperatorKey, "operator-key", "",
		"private SSH key that founds core, making the instance answerable to its holder (default: from git's user.signingkey)")
	fs.StringVar(&opts.OperatorPrincipal, "operator-principal", "",
		"identity the operator key signs as (default: git's user.email)")
	fs.BoolVar(&opts.SelfSigned, "self-signed", false,
		"found core with the instance's own agent key, without looking for an operator key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return runInit(stdout, dir, opts)
}

// bundledTalentFiles reads the talent set compiled into the binary, keyed by
// the repo-relative path it takes inside core.
//
// They are handed to the birth commit rather than written to disk first, so a
// fresh instance never has a moment where its behaviour definitions exist
// unsigned. That also means they are covered by the operator key when one
// founds the instance, rather than by whoever commits next.
func bundledTalentFiles() (map[string]string, error) {
	out := make(map[string]string)
	err := fs.WalkDir(talents.DefaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := filepath.Base(path)
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		data, err := talents.DefaultFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		out["talents/"+name] = string(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect bundled talents: %w", err)
	}
	return out, nil
}
