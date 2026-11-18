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

// Plan item A3, section 17.5: the invariant checklist. An entry names the
// tests that hold it, or the backlog item whose pull request brings its test
// with the fix; never both, never neither.

func TestAChecklistEntryNeedsTestsOrAPendingItem(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestHolds(t *testing.T) {}\n")
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I1
    statement: holds
    tests: [TestHolds]
  - id: I2
    statement: neither
  - id: I3
    statement: both
    tests: [TestHolds]
    pending: B1
`)
	problems, _, err := invariantProblems(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"I2: names no tests and no pending item", "I3: names tests and a pending item"}
	if !slices.Equal(problems, want) {
		t.Fatalf("problems = %q, want %q", problems, want)
	}
}

func TestAChecklistEntryNamesOnlyTestsThatExist(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestHolds(t *testing.T) {}\n")
	writeFile(t, root, "pkg/a/testdata/x/x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestInAFixture(t *testing.T) {}\n")
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I1
    statement: one real, one gone, one only in a fixture
    tests: [TestHolds, TestGone, TestInAFixture]
`)
	problems, _, err := invariantProblems(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"I1: test TestGone does not exist", "I1: test TestInAFixture does not exist"}
	if !slices.Equal(problems, want) {
		t.Fatalf("problems = %q, want %q", problems, want)
	}
}

func TestAChecklistEntryPendsOnlyOnABacklogItem(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I1
    statement: real item
    pending: B1
  - id: I2
    statement: no such item
    pending: Z9
`)
	problems, _, err := invariantProblems(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"I2: pending on Z9, which is not a backlog item"}
	if !slices.Equal(problems, want) {
		t.Fatalf("problems = %q, want %q", problems, want)
	}
}

func TestEveryRequiredInvariantHasAnEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I1-always-a-limit
    statement: covers I1 by its prefix
    pending: B1
  - id: I10
    statement: does not cover I1
    pending: H1
`)
	problems, _, err := invariantProblems(root, []string{"I1", "I2", "I10", "6.4-replaced"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"I2: no entry in the invariants checklist", "6.4-replaced: no entry in the invariants checklist"}
	if !slices.Equal(problems, want) {
		t.Fatalf("problems = %q, want %q", problems, want)
	}
}

func TestDuplicateChecklistIDsAreRefused(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I1
    statement: first
    pending: B1
  - id: I1
    statement: second
    pending: B1
`)
	problems, _, err := invariantProblems(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(problems, []string{"I1: listed twice"}) {
		t.Fatalf("problems = %q", problems)
	}
}

// Section 19.D: every run of the check lists the entries still pending, so
// they stay in front of whoever reads the output.
func TestThePendingEntriesAreListed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "FEATURES.yaml", `invariants:
  - id: I7-drain
    statement: drain on stop
    pending: C2
`)
	_, pending, err := invariantProblems(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pending, []string{"I7-drain (C2): drain on stop"}) {
		t.Fatalf("pending = %q", pending)
	}
}

// The repository's own checklist is complete and correct.
func TestTheInvariantChecklistIsComplete(t *testing.T) {
	problems, _, err := invariantProblems(filepath.Join("..", "..", ".."), requiredInvariants)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("FEATURES.yaml invariants: %s", p)
	}
}
