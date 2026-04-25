package app

import (
	"log/slog"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func testModelExperienceRouter() *router.Router {
	return router.NewRouter(slog.Default(), router.Config{
		DefaultModel: "spark/gpt-oss:20b",
		Models: []router.Model{
			{
				Name:           "deepslate/google/gemma-3-4b",
				UpstreamModel:  "google/gemma-3-4b",
				Provider:       "lmstudio",
				ResourceID:     "deepslate",
				SupportsTools:  true,
				SupportsImages: true,
				ContextWindow:  131072,
				Speed:          6,
				Quality:        8,
				CostTier:       0,
			},
			{
				Name:          "spark/gpt-oss:20b",
				UpstreamModel: "gpt-oss:20b",
				Provider:      "ollama",
				ResourceID:    "spark",
				SupportsTools: true,
				ContextWindow: 8192,
				Speed:         8,
				Quality:       6,
				CostTier:      0,
			},
		},
		MaxAuditLog: 10,
	})
}

func TestModelExperienceStoreSaveAndLoadIntoRouter(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	store := newModelExperienceStore(op)

	source := testModelExperienceRouter()
	source.ReplaceExperience(map[string]router.DeploymentStats{
		"deepslate/google/gemma-3-4b": {
			Provider:      "lmstudio",
			Resource:      "deepslate",
			UpstreamModel: "google/gemma-3-4b",
			Requests:      6,
			Successes:     5,
			Failures:      1,
			AvgLatencyMs:  3200,
			AvgTokensUsed: 880,
		},
	})
	if err := store.SaveFrom(source); err != nil {
		t.Fatalf("SaveFrom: %v", err)
	}

	target := testModelExperienceRouter()
	if err := store.LoadInto(target, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	stats := target.GetStats()
	dep := stats.DeploymentStats["deepslate/google/gemma-3-4b"]
	if dep.Requests != 6 || dep.Successes != 5 || dep.Failures != 1 {
		t.Fatalf("loaded deployment stats = %+v, want requests=6 successes=5 failures=1", dep)
	}
	if stats.TotalRequests != 6 {
		t.Fatalf("TotalRequests = %d, want 6", stats.TotalRequests)
	}
	if stats.ModelCounts["deepslate/google/gemma-3-4b"] != 6 {
		t.Fatalf("ModelCounts[deepslate/google/gemma-3-4b] = %d, want 6", stats.ModelCounts["deepslate/google/gemma-3-4b"])
	}
}

func TestModelExperienceStoreLoadIntoSkipsInvalidEntries(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	if err := op.Set(modelRegistryExperienceNamespace, "spark/gpt-oss:20b", "{not-json"); err != nil {
		t.Fatalf("op.Set: %v", err)
	}

	store := newModelExperienceStore(op)
	rtr := testModelExperienceRouter()
	if err := store.LoadInto(rtr, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	stats := rtr.GetStats()
	if stats.TotalRequests != 0 {
		t.Fatalf("TotalRequests = %d, want 0", stats.TotalRequests)
	}
	if dep := stats.DeploymentStats["spark/gpt-oss:20b"]; dep.Requests != 0 {
		t.Fatalf("spark deployment stats = %+v, want zero values", dep)
	}
}
