package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/modeleval"
)

type evalReport struct {
	SchemaVersion          int              `json:"schema_version"`
	CreatedAt              string           `json:"created_at"`
	ContainsProductionData bool             `json:"contains_production_data"`
	Notice                 string           `json:"notice"`
	SnapshotPath           string           `json:"snapshot_path"`
	Target                 evalTarget       `json:"target"`
	Summary                evalSummary      `json:"summary"`
	Cases                  []evalCaseResult `json:"cases"`
}

type evalTarget struct {
	BaseURL            string `json:"base_url"`
	Model              string `json:"model"`
	Provider           string `json:"provider,omitempty"`
	Family             string `json:"family,omitempty"`
	TrainedForToolUse  bool   `json:"trained_for_tool_use"`
	ToolContractMode   string `json:"tool_contract_mode"`
	InteractionProfile string `json:"interaction_profile"`
	Streaming          bool   `json:"streaming"`
	MaxOutputTokens    int    `json:"max_output_tokens"`
}

type evalSummary struct {
	Cases               int   `json:"cases"`
	Responses           int   `json:"responses"`
	Errors              int   `json:"errors"`
	DecisionMatches     int   `json:"decision_matches"`
	ToolNameMatches     int   `json:"tool_name_matches"`
	ToolArgumentMatches int   `json:"tool_argument_matches"`
	ReferenceMatches    int   `json:"reference_matches"`
	PromptAdaptedCases  int   `json:"prompt_adapted_cases"`
	TotalDurationMS     int64 `json:"total_duration_ms"`
}

type evalCaseResult struct {
	CaseID         string          `json:"case_id"`
	ReferenceModel string          `json:"reference_model"`
	CandidateModel string          `json:"candidate_model,omitempty"`
	PromptAdapted  bool            `json:"prompt_adapted"`
	Score          modeleval.Score `json:"score"`
	Expected       llm.Message     `json:"expected"`
	Actual         llm.Message     `json:"actual"`
	StopReason     string          `json:"stop_reason,omitempty"`
	InputTokens    int             `json:"input_tokens,omitempty"`
	OutputTokens   int             `json:"output_tokens,omitempty"`
	DurationMS     int64           `json:"duration_ms"`
	Error          string          `json:"error,omitempty"`
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	snapshotPath := fs.String("snapshot", "", "private snapshot to replay (required)")
	baseURL := fs.String("base-url", "", "OpenAI-compatible server base URL (required)")
	model := fs.String("model", "", "model identifier sent to the server (required)")
	output := fs.String("output", "", "private report path (default: beside snapshot)")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable containing the optional API key")
	provider := fs.String("provider", "openai_compat", "provider/family hint for prompt adaptation")
	family := fs.String("family", "", "model-family hint for prompt adaptation")
	trainedForToolUse := fs.Bool("trained-for-tool-use", true, "declare native provider tool-call training")
	contractMode := fs.String("tool-contract", "auto", "tool contract: auto | exact | native | raw-text")
	streaming := fs.Bool("stream", true, "exercise the streaming parser path")
	caseTimeout := fs.Duration("case-timeout", 10*time.Minute, "deadline for each replay case")
	maxOutputTokens := fs.Int("max-output-tokens", 1024, "per-case output ceiling")
	limit := fs.Int("limit", 0, "optional maximum number of cases (0 = all)")
	overwrite := fs.Bool("overwrite", false, "replace an existing private report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*snapshotPath) == "" || strings.TrimSpace(*baseURL) == "" || strings.TrimSpace(*model) == "" {
		return fmt.Errorf("run requires -snapshot, -base-url, and -model")
	}
	if *caseTimeout <= 0 {
		return fmt.Errorf("case-timeout must be positive")
	}
	if *maxOutputTokens <= 0 {
		return fmt.Errorf("max-output-tokens must be positive")
	}
	if *limit < 0 {
		return fmt.Errorf("limit must not be negative")
	}

	snapshot, err := loadSnapshot(*snapshotPath)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(snapshot.Cases) {
		snapshot.Cases = snapshot.Cases[:*limit]
	}
	profile, err := interactionProfile(*contractMode, llm.ModelProfileInput{
		Provider:          *provider,
		Model:             *model,
		Family:            *family,
		TrainedForToolUse: *trainedForToolUse,
	})
	if err != nil {
		return err
	}

	apiKey := ""
	if *apiKeyEnv != "" {
		apiKey = os.Getenv(*apiKeyEnv)
		if apiKey == "" {
			return fmt.Errorf("API key environment variable %q is empty", *apiKeyEnv)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := providers.NewOpenAICompatClient(*baseURL, apiKey, *provider, "model-eval", logger, 0)

	report := evalReport{
		SchemaVersion:          1,
		CreatedAt:              time.Now().UTC().Format(time.RFC3339Nano),
		ContainsProductionData: true,
		Notice:                 productionDataNotice,
		SnapshotPath:           *snapshotPath,
		Target: evalTarget{
			BaseURL:            *baseURL,
			Model:              *model,
			Provider:           *provider,
			Family:             *family,
			TrainedForToolUse:  *trainedForToolUse,
			ToolContractMode:   *contractMode,
			InteractionProfile: profile.Name,
			Streaming:          *streaming,
			MaxOutputTokens:    *maxOutputTokens,
		},
		Cases: make([]evalCaseResult, 0, len(snapshot.Cases)),
	}

	for _, case_ := range snapshot.Cases {
		messages := case_.Messages
		adapted := false
		if *contractMode != "exact" {
			messages, adapted = modeleval.ApplyToolCallingProfile(case_.Messages, case_.PromptSections, profile)
		}
		result := evalCaseResult{
			CaseID:         case_.ID,
			ReferenceModel: case_.ReferenceModel,
			PromptAdapted:  adapted,
			Expected:       case_.Expected,
		}
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), *caseTimeout)
		ctx = llm.WithMaxOutputTokens(ctx, *maxOutputTokens)
		var response *llm.ChatResponse
		if *streaming {
			response, err = client.ChatStream(ctx, *model, messages, case_.Tools, func(llm.StreamEvent) {})
		} else {
			response, err = client.Chat(ctx, *model, messages, case_.Tools)
		}
		cancel()
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			report.Summary.Errors++
		} else {
			result.CandidateModel = response.Model
			result.Actual = response.Message
			result.StopReason = response.StopReason
			result.InputTokens = response.InputTokens
			result.OutputTokens = response.OutputTokens
			result.Score = modeleval.Evaluate(case_.Expected, response.Message)
			report.Summary.Responses++
			if result.Score.DecisionMatch {
				report.Summary.DecisionMatches++
			}
			if result.Score.ToolNamesMatch {
				report.Summary.ToolNameMatches++
			}
			if result.Score.ToolArgumentsMatch {
				report.Summary.ToolArgumentMatches++
			}
			if result.Score.ReferenceMatch {
				report.Summary.ReferenceMatches++
			}
		}
		if adapted {
			report.Summary.PromptAdaptedCases++
		}
		report.Summary.TotalDurationMS += result.DurationMS
		report.Cases = append(report.Cases, result)
	}
	report.Summary.Cases = len(report.Cases)

	if *output == "" {
		base := strings.TrimSuffix(filepath.Base(*snapshotPath), filepath.Ext(*snapshotPath))
		*output = filepath.Join(filepath.Dir(*snapshotPath), base+"-"+safeFilename(*model)+".thane-eval-report.json")
	}
	if err := writePrivateJSON(*output, report, *overwrite); err != nil {
		return err
	}
	absOutput, _ := filepath.Abs(*output)
	fmt.Fprintf(os.Stderr, "Replayed %d cases against %s: responses=%d errors=%d decision_match=%d reference_match=%d report=%s\n",
		report.Summary.Cases, *model, report.Summary.Responses, report.Summary.Errors,
		report.Summary.DecisionMatches, report.Summary.ReferenceMatches, absOutput)
	return nil
}

func loadSnapshot(path string) (modeleval.Snapshot, error) {
	const maxSnapshotSize = 512 << 20

	file, err := os.Open(path)
	if err != nil {
		return modeleval.Snapshot{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return modeleval.Snapshot{}, fmt.Errorf("stat snapshot: %w", err)
	}
	if info.Size() > maxSnapshotSize {
		return modeleval.Snapshot{}, fmt.Errorf("snapshot is %d bytes; maximum is %d", info.Size(), maxSnapshotSize)
	}
	var snapshot modeleval.Snapshot
	decoder := json.NewDecoder(io.LimitReader(file, maxSnapshotSize))
	if err := decoder.Decode(&snapshot); err != nil {
		return modeleval.Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return modeleval.Snapshot{}, fmt.Errorf("decode snapshot: unexpected trailing JSON value")
		}
		return modeleval.Snapshot{}, fmt.Errorf("decode snapshot trailing data: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return modeleval.Snapshot{}, err
	}
	return snapshot, nil
}

func interactionProfile(mode string, input llm.ModelProfileInput) (llm.ModelInteractionProfile, error) {
	switch mode {
	case "auto":
		return llm.ProfileForModel(input), nil
	case "exact":
		return llm.DefaultModelInteractionProfile(), nil
	case "native":
		return llm.DefaultModelInteractionProfile(), nil
	case "raw-text":
		profile := llm.DefaultModelInteractionProfile()
		profile.Name = "forced_raw_text_tools"
		profile.ToolCallStyle = llm.ToolCallStyleRawTextJSON
		return profile, nil
	default:
		return llm.ModelInteractionProfile{}, fmt.Errorf("tool-contract must be auto, exact, native, or raw-text")
	}
}

func safeFilename(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "model"
	}
	return out.String()
}
