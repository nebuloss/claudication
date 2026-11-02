package recordings

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

// The main store's rule, for this file of its own (internal/store,
// TestEveryWriteMarksATopic): every function that writes tells the hub what
// changed — or says, with a //live:none comment and its reason, that no
// screen shows what it writes.
func TestEveryWriteMarksATopic(t *testing.T) {
	writeSQL := regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|REPLACE)\b`)
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
			if !ok || fn.Body == nil {
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
			if !strings.Contains(body, ".Changed(") && !strings.Contains(body, "//live:none") {
				t.Errorf("%s: %s writes and marks no topic (hub.Changed, or //live:none and why)", name, fn.Name.Name)
			}
		}
	}
}
