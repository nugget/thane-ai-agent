package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	r := New(map[string]string{
		"kb":         "/data/vault",
		"scratchpad": "/data/scratch",
	})

	tests := []struct {
		name string
		path string
		want string
	}{
		{"kb prefix", "kb:foo.md", filepath.Join("/data/vault", "foo.md")},
		{"kb nested", "kb:dossiers/cat.md", filepath.Join("/data/vault", "dossiers", "cat.md")},
		{"scratchpad prefix", "scratchpad:dev-status", filepath.Join("/data/scratch", "dev-status")},
		{"bare kb prefix", "kb:", "/data/vault"},
		{"bare scratchpad prefix", "scratchpad:", "/data/scratch"},
		{"absolute path unchanged", "/absolute/path", "/absolute/path"},
		{"relative path unchanged", "relative/path", "relative/path"},
		{"empty string unchanged", "", ""},
		{"tilde unchanged", "~/notes.md", "~/notes.md"},
		{"no match", "unknown:foo", "unknown:foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(tt.path)
			if err != nil {
				t.Fatalf("Resolve(%q) error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolve_NilReceiver(t *testing.T) {
	var r *Resolver
	got, err := r.Resolve("kb:foo.md")
	if err != nil {
		t.Fatalf("nil Resolve error: %v", err)
	}
	if got != "kb:foo.md" {
		t.Errorf("nil Resolve(%q) = %q, want unchanged", "kb:foo.md", got)
	}
}

func TestResolve_LongerPrefixFirst(t *testing.T) {
	r := New(map[string]string{
		"kb":    "/short",
		"kbase": "/long",
	})

	got, err := r.Resolve("kbase:doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/long", "doc.md") {
		t.Errorf("expected longer prefix to match, got %q", got)
	}

	got, err = r.Resolve("kb:doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/short", "doc.md") {
		t.Errorf("expected shorter prefix to match, got %q", got)
	}
}

func TestNew_EmptyMap(t *testing.T) {
	if r := New(nil); r != nil {
		t.Error("New(nil) should return nil")
	}
	if r := New(map[string]string{}); r != nil {
		t.Error("New(empty) should return nil")
	}
}

func TestHasPrefix(t *testing.T) {
	r := New(map[string]string{"kb": "/vault"})

	tests := []struct {
		path string
		want bool
	}{
		{"kb:foo.md", true},
		{"kb:", true},
		{"/absolute", false},
		{"relative", false},
		{"", false},
		{"unknown:bar", false},
	}

	for _, tt := range tests {
		if got := r.HasPrefix(tt.path); got != tt.want {
			t.Errorf("HasPrefix(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestHasPrefix_NilReceiver(t *testing.T) {
	var r *Resolver
	if r.HasPrefix("kb:foo") {
		t.Error("nil HasPrefix should return false")
	}
}

func TestPrefixes(t *testing.T) {
	r := New(map[string]string{
		"scratchpad": "/scratch",
		"kb":         "/vault",
		"archive":    "/archive",
	})

	got := r.Prefixes()
	want := []string{"archive", "kb", "scratchpad"}
	if len(got) != len(want) {
		t.Fatalf("Prefixes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Prefixes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPrefixes_NilReceiver(t *testing.T) {
	var r *Resolver
	if got := r.Prefixes(); got != nil {
		t.Errorf("nil Prefixes() = %v, want nil", got)
	}
}

func TestExpandHome(t *testing.T) {
	// Verify that ~ paths in base directories are expanded at
	// construction time by checking that the resolved path does not
	// contain a tilde.
	r := New(map[string]string{"kb": "~/vault"})
	if r == nil {
		t.Fatal("expected non-nil resolver")
	}

	got, err := r.Resolve("kb:doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if got == "~/vault/doc.md" {
		t.Error("expected tilde expansion in base directory, but got literal ~")
	}
	// The path should be absolute (home dir is always absolute).
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path after tilde expansion, got %q", got)
	}
}

func TestResolverDynamicRepositoryRootLifecycle(t *testing.T) {
	configured := t.TempDir()
	repository := t.TempDir()
	r := New(map[string]string{"kb": configured})

	root := Root{
		Name:     "thanecode",
		Path:     repository,
		Kind:     RootKindRepository,
		ReadOnly: true,
		Owner:    "subscription-1",
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("Register: %v", err)
	}
	resolved, matched, ok := r.ResolveRoot("thanecode:internal/app/new.go")
	if !ok || matched != root {
		t.Fatalf("ResolveRoot matched %+v, ok=%v, want %+v", matched, ok, root)
	}
	if want := filepath.Join(repository, "internal", "app", "new.go"); resolved != want {
		t.Fatalf("ResolveRoot = %q, want %q", resolved, want)
	}
	if err := r.Register(Root{Name: "thanecode", Path: t.TempDir(), Kind: RootKindRepository, Owner: "subscription-2"}); err == nil {
		t.Fatal("Register replaced an existing named root")
	}
	if r.Unregister("thanecode", "subscription-2") {
		t.Fatal("Unregister removed a root owned by another subscription")
	}
	if !r.Unregister("thanecode", "subscription-1") {
		t.Fatal("Unregister did not remove the owning subscription's root")
	}
	if _, ok := r.Root("thanecode"); ok {
		t.Fatal("root remains registered")
	}
	if _, ok := r.Root("kb"); !ok {
		t.Fatal("dynamic unregistration affected configured root")
	}
}

func TestResolverRegisterCreatedReportsInsertionOwnership(t *testing.T) {
	t.Parallel()

	r := New(map[string]string{"core": t.TempDir()})
	root := Root{Name: "thanecode", Path: t.TempDir(), Kind: RootKindRepository, ReadOnly: true, Owner: "sub"}
	created, err := r.RegisterCreated(root)
	if err != nil || !created {
		t.Fatalf("first RegisterCreated = %v, %v; want created", created, err)
	}
	created, err = r.RegisterCreated(root)
	if err != nil || created {
		t.Fatalf("second RegisterCreated = %v, %v; want existing", created, err)
	}
}

func TestRootForPathUsesNormalizedSpecificity(t *testing.T) {
	outer, err := os.MkdirTemp(".", ".resolver-roots-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outer) })
	absOuter, err := filepath.Abs(outer)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	inner := filepath.Join(absOuter, "knowledge")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	r := New(map[string]string{
		"outer": absOuter,
		"inner": filepath.Join(".", filepath.Base(absOuter), "knowledge"),
	})
	root, ok := r.RootForPath(filepath.Join(inner, "note.md"))
	if !ok || root.Name != "inner" {
		t.Fatalf("RootForPath = %+v, ok=%v; want normalized inner root", root, ok)
	}
}

func TestContainsPathResolvesSymlinkedAncestorsForMissingDescendant(t *testing.T) {
	root := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if !ContainsPath(link, filepath.Join(link, "missing", "child.txt")) {
		t.Fatal("ContainsPath rejected a missing descendant through a symlinked ancestor")
	}
	if ContainsPath(link, filepath.Join(linkParent, "root-sibling", "child.txt")) {
		t.Fatal("ContainsPath accepted a sibling with a shared string prefix")
	}
}
