package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
)

func TestAARCH01Boundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "init" {
				t.Errorf("Initializer in %s", path)
			}
			if values, ok := decl.(*ast.GenDecl); ok && values.Tok == token.VAR {
				for _, spec := range values.Specs {
					if v, ok := spec.(*ast.ValueSpec); ok && len(v.Values) > 0 {
						t.Errorf("Fallible package variable initializer in %s", path)
					}
				}
			}
		}
		ast.Inspect(f, func(node ast.Node) bool {
			if s, ok := node.(*ast.SelectorExpr); ok {
				if x, ok := s.X.(*ast.Ident); ok && x.Name == "os" && (s.Sel.Name == "Exit" || s.Sel.Name == "Stdout") {
					t.Errorf("Deep public output/exit in %s", path)
				}
			}
			return true
		})
		if strings.Contains(path, filepath.Join("internal", "review")) {
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == "os/exec" || strings.Contains(p, "toml") || strings.HasSuffix(p, "/process") || strings.HasSuffix(p, "/gashki") || strings.HasSuffix(p, "/agent") {
					t.Errorf("Impure review dependency %s", p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalTypedData(t *testing.T) {
	x, err := contract.Canonical(struct {
		Z int    `json:"z"`
		A string `json:"a"`
	}{8, "<&"})
	if err != nil {
		t.Fatal(err)
	}
	if string(x) != `{"a":"<&","z":8}` {
		t.Fatalf("%s", x)
	}
	if _, err := contract.Canonical(map[string]any{"x": 1.5}); err == nil {
		t.Fatal("Float accepted")
	}
}
