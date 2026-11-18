package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Plan item Q3: every fuzz target in the repository runs in the nightly
// workflow, so a new parser's target cannot be added and then never fuzzed.
func TestEveryFuzzTargetRunsNightly(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	nightly, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "nightly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	targets := fuzzTargets(t, root)
	if len(targets) == 0 {
		t.Fatal("found no fuzz targets; the search is broken")
	}
	for _, name := range targets {
		if !strings.Contains(string(nightly), "name: "+name+" }") {
			t.Errorf("%s is not in .github/workflows/nightly.yml", name)
		}
	}
}

func fuzzTargets(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Fuzz") {
				names = append(names, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}
