package agentctx

import (
	"math"
	"strings"
	"testing"
)

func TestContextAdvertisementValidate(t *testing.T) {
	t.Parallel()

	valid := ContextAdvertisement{
		ID:      "alice",
		Source:  "archivist",
		Kind:    "dossier",
		Ref:     "dossiers:alice.md",
		Bucket:  ContextBucketRelated,
		Summary: "A maintained subject dossier.",
		Matches: []ContextMatchSignal{{Kind: ContextMatchExactSubject, Strength: 1}},
		Projections: []ContextProjection{
			{Name: "signal", Role: ContextRoleSignal, Format: "text/markdown", EstimatedBytes: 320},
			{Name: "digest", Role: ContextRoleContext, Format: "text/markdown", EstimatedBytes: 2048},
			{Name: "full", Role: ContextRoleDetail, Format: "text/markdown", EstimatedBytes: 8192},
		},
	}

	tests := []struct {
		name    string
		mutate  func(*ContextAdvertisement)
		wantErr string
	}{
		{name: "valid"},
		{name: "missing id", mutate: func(ad *ContextAdvertisement) { ad.ID = " " }, wantErr: "id is required"},
		{name: "padded id", mutate: func(ad *ContextAdvertisement) { ad.ID = " alice " }, wantErr: "stable value"},
		{name: "unknown bucket", mutate: func(ad *ContextAdvertisement) { ad.Bucket = "elsewhere" }, wantErr: "bucket"},
		{name: "unknown match", mutate: func(ad *ContextAdvertisement) { ad.Matches[0].Kind = "vibes" }, wantErr: "matches[0]"},
		{name: "nan strength", mutate: func(ad *ContextAdvertisement) { ad.Matches[0].Strength = math.NaN() }, wantErr: "finite"},
		{name: "zero strength", mutate: func(ad *ContextAdvertisement) { ad.Matches[0].Strength = 0 }, wantErr: "greater than 0"},
		{name: "duplicate projection", mutate: func(ad *ContextAdvertisement) { ad.Projections[1].Name = "signal" }, wantErr: "duplicate name"},
		{name: "unbounded estimate", mutate: func(ad *ContextAdvertisement) { ad.Projections[0].EstimatedBytes = 0 }, wantErr: "estimated_bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ad := valid
			ad.Matches = append([]ContextMatchSignal(nil), valid.Matches...)
			ad.Projections = append([]ContextProjection(nil), valid.Projections...)
			if tt.mutate != nil {
				tt.mutate(&ad)
			}
			err := ad.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
