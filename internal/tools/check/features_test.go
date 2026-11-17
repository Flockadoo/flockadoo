package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Plan item G1: the feature check fails when FEATURES.yaml names a path that
// does not exist, and when a feature lists no code or no tests. A glob must
// match at least one file.

func TestFeatureCheckNamesEveryMissingPath(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "pkg/a/a.go", "pkg/a/a_test.go")
	writeFile(t, root, "FEATURES.yaml", `features:
  - name: Kept
    entry: a.Run
    code: [pkg/a/a.go]
    tests: [pkg/*/a_test.go]
  - name: Moved
    entry: b.Run
    code: [pkg/a/a.go, pkg/b/b.go]
    tests: [pkg/*/b_test.go]
`)
	got, err := featureProblems(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Moved: pkg/b/b.go does not exist", "Moved: pkg/*/b_test.go matches nothing"}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

func TestFeatureCheckNamesAFeatureWithNoCodeOrTests(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "pkg/a/a.go", "pkg/a/a_test.go")
	writeFile(t, root, "FEATURES.yaml", `features:
  - name: Covered
    entry: a.Run
    code: [pkg/a/a.go]
    tests: [pkg/a/a_test.go]
  - name: Untested
    entry: a.Run
    code: [pkg/a/a.go]
    tests: []
  - name: Nowhere
    entry: a.Run
    tests: [pkg/a/a_test.go]
`)
	got, err := featureProblems(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Untested: lists no tests", "Nowhere: lists no code"}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

// A misspelt key would leave a list silently empty, so unknown keys are an
// error rather than ignored.
func TestFeatureCheckRefusesAnUnknownKey(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "pkg/a/a.go", "pkg/a/a_test.go")
	writeFile(t, root, "FEATURES.yaml", `features:
  - name: Typo
    entry: a.Run
    code: [pkg/a/a.go]
    test: [pkg/a/a_test.go]
`)
	if _, err := featureProblems(root); err == nil {
		t.Fatal("a feature with the key 'test' instead of 'tests' was accepted")
	}
}

func TestFeatureCheckNeedsTheFile(t *testing.T) {
	if _, err := featureProblems(t.TempDir()); err == nil {
		t.Fatal("a repository with no FEATURES.yaml passed the feature check")
	}
}

// The repository's own feature map is current; go test ./... runs this.
func TestTheFeatureMapIsCurrent(t *testing.T) {
	got, err := featureProblems(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		t.Errorf("FEATURES.yaml: %s", p)
	}
}

func writeFiles(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		writeFile(t, root, p, "")
	}
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
