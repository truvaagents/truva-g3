package orchestration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionAIInvocationsUseCentralBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	invocations := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports for %s: %v", name, err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			if path == "github.com/truvaagents/truva-g3/ai" || strings.HasPrefix(path, "github.com/truvaagents/truva-g3/ai/") {
				t.Errorf("%s imports provider-owning AI module %q", name, path)
			}
		}

		parsed, err = parser.ParseFile(files, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.SelectorExpr:
				if expression.Sel.Name == "GenerateResponse" || expression.Sel.Name == "StreamResponse" {
					t.Errorf("%s:%d invokes %s outside the central AI boundary", name, files.Position(expression.Pos()).Line, expression.Sel.Name)
				}
			case *ast.CompositeLit:
				identifier, ok := expression.Type.(*ast.Ident)
				if !ok || identifier.Name != "aiInvocation" {
					break
				}
				invocations++
				purpose := ""
				for _, element := range expression.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok || key.Name != "Purpose" {
						continue
					}
					value, ok := pair.Value.(*ast.BasicLit)
					if ok && value.Kind == token.STRING {
						purpose, _ = strconv.Unquote(value.Value)
					}
				}
				if strings.TrimSpace(purpose) == "" {
					t.Errorf("%s:%d has an aiInvocation without a stable literal Purpose", name, files.Position(expression.Pos()).Line)
				}
			}
			return true
		})
	}
	if invocations == 0 {
		t.Fatal("no production aiInvocation values found")
	}
}
