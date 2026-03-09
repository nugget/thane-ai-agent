package carddav

import (
	"testing"

	"github.com/google/uuid"
)

func TestObjectPath(t *testing.T) {
	id := uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef")
	got := objectPath(id)
	want := "/carddav/default/01234567-89ab-cdef-0123-456789abcdef.vcf"
	if got != want {
		t.Errorf("objectPath() = %q, want %q", got, want)
	}
}

func TestContactIDFromPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "valid path",
			path: "/carddav/default/01234567-89ab-cdef-0123-456789abcdef.vcf",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
		{
			name:    "invalid UUID",
			path:    "/carddav/default/not-a-uuid.vcf",
			wantErr: true,
		},
		{
			name: "no extension still parses",
			path: "/carddav/default/01234567-89ab-cdef-0123-456789abcdef",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := contactIDFromPath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("contactIDFromPath() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}
