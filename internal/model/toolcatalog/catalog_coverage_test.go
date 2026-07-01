package toolcatalog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNativeToolLiteralsAreCatalogued is the CI-time guard against the
// doc_history / loop_reparent gap: a tool that is registered but absent from
// the catalog carries no capability tag and is silently never offered to a
// tag-gated model. Registry.Register enriches a tool's tags from the catalog
// and defaults an unrecognized tool to the native source with no tags, so the
// omission is invisible until a human notices the tool missing in production.
//
// This scans the repository source for tools.Tool composite literals with a
// static Name and asserts each is in the catalog — catching the omission
// before a production push, for every tool in every package, with no per-family
// list to maintain. It is the CI complement to the runtime guard in
// finalizeCapabilityTags (uncataloguedNativeTools).
//
// Excluded, because they are legitimately absent from the static catalog:
//   - literals that set Source (provider/dynamic tools: mcp, companion, …),
//   - literals that set Core:true (exempt from capability-tag filtering),
//   - literals whose Name is built dynamically (not a string constant).
func TestNativeToolLiteralsAreCatalogued(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	var problems []string
	walkRoot := filepath.Join(root, "internal")
	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isToolLiteralType(lit.Type) {
				return true
			}
			name, ok := stringFieldValue(lit, "Name")
			if !ok {
				return true // dynamic name — inherently not in the static catalog
			}
			if hasField(lit, "Source") || boolFieldIsTrue(lit, "Core") {
				return true // provider/dynamic, or tag-exempt
			}
			if _, ok := LookupBuiltinToolSpec(name); !ok {
				pos := fset.Position(lit.Pos())
				rel, _ := filepath.Rel(root, pos.Filename)
				problems = append(problems, name+" ("+rel+":"+strconv.Itoa(pos.Line)+")")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/: %v", err)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("native tool(s) registered but missing from the catalog — they carry no capability tag and will not be offered to the model.\n"+
			"Add each to internal/model/toolcatalog/catalog.go with the right tags (or set Source/Core if it is intentionally uncatalogued):\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// isToolLiteralType reports whether a composite-literal type is tools.Tool:
// the bare `Tool` (inside package tools) or the qualified `tools.Tool`
// (everywhere else).
func isToolLiteralType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Tool"
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		return ok && pkg.Name == "tools" && t.Sel.Name == "Tool"
	}
	return false
}

// field returns the value expression for a named field of a composite literal.
func field(lit *ast.CompositeLit, name string) (ast.Expr, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == name {
			return kv.Value, true
		}
	}
	return nil, false
}

func hasField(lit *ast.CompositeLit, name string) bool {
	_, ok := field(lit, name)
	return ok
}

func stringFieldValue(lit *ast.CompositeLit, name string) (string, bool) {
	v, ok := field(lit, name)
	if !ok {
		return "", false
	}
	bl, ok := v.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func boolFieldIsTrue(lit *ast.CompositeLit, name string) bool {
	v, ok := field(lit, name)
	if !ok {
		return false
	}
	id, ok := v.(*ast.Ident)
	return ok && id.Name == "true"
}

// repoRoot walks up from the test's working directory to the module root
// (the directory containing go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs cwd: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate go.mod above the test directory")
		}
		dir = parent
	}
}
