package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeSQL is a statement that changes rows.
var writeSQL = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|REPLACE)\b`)

// notWrites are methods that run such statements on the schema itself, not
// on data a screen shows: migrating, reclaiming, vacuuming.
var notWrites = map[string]bool{
	"migrate": true, "reclaim": true, "Vacuum": true, "Optimize": true,
}

// Every function that writes marks the topic it changed — or says, with a
// //live:none comment and its reason, that no screen shows what it writes —
// so a write added tomorrow cannot leave a screen stale until someone
// notices (docs/data-architecture.md).
func TestEveryWriteMarksATopic(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || notWrites[fn.Name.Name] {
				continue
			}
			writes := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && writeSQL.MatchString(lit.Value) {
					writes = true
				}
				return !writes
			})
			if !writes {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			if !strings.Contains(body, ".changed(") && !strings.Contains(body, "//live:none") {
				t.Errorf("%s: %s writes and marks no topic (s.changed, or //live:none and why)", name, fn.Name.Name)
			}
		}
	}
}
