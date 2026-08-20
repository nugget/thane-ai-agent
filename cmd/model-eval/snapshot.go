package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/modeleval"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

const productionDataNotice = "Contains private production prompts, messages, and tool results. Best-effort retention limits are not redaction. Do not publish or commit this artifact."

func cmdSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	defaultDB := filepath.Join(filepath.Dir(defaultEvalDir()), "archive", "logs.db")
	logsDB := fs.String("logs-db", defaultDB, "path to production logs.db")
	output := fs.String("output", "", "private snapshot path (default: ~/Thane/evals/snapshot-<time>.thane-eval.json)")
	since := fs.Duration("since", 7*24*time.Hour, "include requests retained within this window")
	limit := fs.Int("limit", 100, "maximum retained requests to inspect")
	model := fs.String("model", "", "optional reference-model filter")
	includeExhausted := fs.Bool("include-exhausted", false, "include requests that exhausted their run budget")
	overwrite := fs.Bool("overwrite", false, "replace an existing private output file")
	var requestIDs stringList
	fs.Var(&requestIDs, "request-id", "specific request ID to export (repeatable; bypasses since/model selection)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *limit <= 0 || *limit > 1000 {
		return fmt.Errorf("snapshot limit must be between 1 and 1000")
	}
	if *since <= 0 {
		return fmt.Errorf("snapshot since window must be positive")
	}
	if *output == "" {
		*output = filepath.Join(defaultEvalDir(), "snapshot-"+time.Now().UTC().Format("20060102T150405Z")+".thane-eval.json")
	}

	db, err := openLogsReadOnly(*logsDB)
	if err != nil {
		return err
	}
	defer db.Close()
	if !database.HasColumn(db, "log_request_content", "model_calls_json") {
		return fmt.Errorf("logs database predates model-call capture; deploy this build and collect new retained requests before exporting")
	}

	ids := []string(requestIDs)
	if len(ids) == 0 {
		ids, err = logging.QueryRecentModelCallRequestIDs(context.Background(), db, time.Now().Add(-*since), *model, *limit)
		if err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("no retained model calls matched; deploy capture support and verify logging.retain_content is enabled")
	}

	snapshot := modeleval.Snapshot{
		SchemaVersion:          modeleval.SnapshotSchemaVersion,
		CreatedAt:              time.Now().UTC().Format(time.RFC3339Nano),
		ContainsProductionData: true,
		Notice:                 productionDataNotice,
		Cases:                  []modeleval.Case{},
	}
	skippedTruncated := 0
	skippedImages := 0
	skippedExhausted := 0
	skippedMissing := 0
	for _, requestID := range ids {
		detail, err := logging.QueryRequestDetail(db, requestID)
		if err != nil {
			return fmt.Errorf("query request %s: %w", requestID, err)
		}
		if detail == nil || len(detail.ModelCalls) == 0 {
			skippedMissing++
			continue
		}
		if detail.Exhausted && !*includeExhausted {
			skippedExhausted += len(detail.ModelCalls)
			continue
		}
		for _, call := range detail.ModelCalls {
			case_, err := modeleval.CaseFromModelCall(requestID, call)
			switch {
			case errors.Is(err, modeleval.ErrTruncatedInput):
				skippedTruncated++
				continue
			case errors.Is(err, modeleval.ErrImageInput):
				skippedImages++
				continue
			case err != nil:
				return fmt.Errorf("build case %s/%d: %w", requestID, call.Iteration, err)
			}
			snapshot.Cases = append(snapshot.Cases, case_)
		}
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := writePrivateJSON(*output, snapshot, *overwrite); err != nil {
		return err
	}
	absOutput, _ := filepath.Abs(*output)
	fmt.Fprintf(os.Stderr, "Wrote %d private model-call cases to %s (skipped: truncated=%d images=%d exhausted=%d missing=%d)\n",
		len(snapshot.Cases), absOutput, skippedTruncated, skippedImages, skippedExhausted, skippedMissing)
	return nil
}
