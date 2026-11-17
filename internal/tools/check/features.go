package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// featureMap is FEATURES.yaml: what each feature is, where it lives and what
// tests it (plan item G1, section 19.B).
type featureMap struct {
	Features   []feature   `yaml:"features"`
	Invariants []invariant `yaml:"invariants"`
}

// invariant is one entry of the checklist (plan section 17.5): it names the
// tests that hold it, or the backlog item whose pull request brings its test
// together with the fix.
type invariant struct {
	ID        string   `yaml:"id"`
	Statement string   `yaml:"statement"`
	Tests     []string `yaml:"tests"`
	Pending   string   `yaml:"pending"`
	Notes     string   `yaml:"notes"`
}

type feature struct {
	Name       string   `yaml:"name"`
	Entry      string   `yaml:"entry"`
	Code       []string `yaml:"code"`
	Tests      []string `yaml:"tests"`
	Invariants []string `yaml:"invariants"`
	Notes      string   `yaml:"notes"`
}

// featureProblems lists, one line each, every path in root's FEATURES.yaml
// that does not exist, every glob that matches nothing, and every feature
// that lists no code or no tests. An unknown key is an error: a misspelt
// "tests" would otherwise leave the list silently empty.
func featureProblems(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "FEATURES.yaml"))
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m featureMap
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("FEATURES.yaml: %w", err)
	}
	var problems []string
	for _, f := range m.Features {
		if len(f.Tests) == 0 {
			problems = append(problems, f.Name+": lists no tests")
		}
		if len(f.Code) == 0 {
			problems = append(problems, f.Name+": lists no code")
		}
		for _, p := range append(append([]string{}, f.Code...), f.Tests...) {
			full := filepath.Join(root, filepath.FromSlash(p))
			if strings.ContainsAny(p, "*?[") {
				matches, err := filepath.Glob(full)
				if err != nil {
					return nil, fmt.Errorf("%s: %s: %w", f.Name, p, err)
				}
				if len(matches) == 0 {
					problems = append(problems, f.Name+": "+p+" matches nothing")
				}
				continue
			}
			if _, err := os.Stat(full); err != nil {
				problems = append(problems, f.Name+": "+p+" does not exist")
			}
		}
	}
	return problems, nil
}

func checkFeatures(_ context.Context, o options, w io.Writer) error {
	problems, err := featureProblems(o.dir)
	if err != nil {
		return err
	}
	more, pending, err := invariantProblems(o.dir, o.requiredInvariants)
	if err != nil {
		return err
	}
	problems = append(problems, more...)
	if len(pending) > 0 {
		_, _ = fmt.Fprintf(w, "%d invariants are pending; each test arrives with the item named:\n%s\n", len(pending), strings.Join(pending, "\n"))
	}
	if len(problems) > 0 {
		_, _ = fmt.Fprintf(w, "update FEATURES.yaml with the code:\n%s\n", strings.Join(problems, "\n"))
		return fmt.Errorf("%d problems in FEATURES.yaml", len(problems))
	}
	return nil
}

// requiredInvariants must each have at least one checklist entry (its own id,
// or the id followed by "-" and a qualifier): the invariants of section 13
// (I13 cannot be tested in the client), the lifecycle rows of 6.4 and the
// properties of 7.3.
var requiredInvariants = []string{
	"I1", "I2", "I3", "I4", "I5", "I6", "I7", "I8", "I9", "I10", "I11", "I12", "I14",
	"6.4-reconnect-after-drop", "6.4-backoff-growth", "6.4-backoff-reset-after-stable",
	"6.4-no-reset-after-short", "6.4-rejected-at-start", "6.4-rejected-later",
	"6.4-draining-gateway", "6.4-upgrade-required", "6.4-half-open", "6.4-replaced",
	"6.4-in-flight-on-drop", "6.4-drain-on-stop", "6.4-accepting-only-when-connected",
	"6.4-tunnel-server-limits",
	"7.3-limit", "7.3-rejected-not-executed", "7.3-released-once", "7.3-cancellation",
	"7.3-failures-no-leak", "7.3-race",
}

// backlogItems are the plan's items (section 20.4); an entry may be pending
// only on one of them.
var backlogItems = map[string]bool{
	"A1": true, "G1": true, "A2": true, "A3": true, "A4": true,
	"Q1": true, "Q2": true, "Q3": true, "Q4": true, "S1": true, "P1": true,
	"B1": true, "B2": true, "B3": true, "C1": true, "C2": true, "C3": true,
	"E1": true, "E2": true, "H1": true, "K1": true, "L1": true,
}

// invariantProblems checks root's invariants checklist: every entry names
// tests that exist, or one pending backlog item, and not both; no id is
// listed twice; and every id in required has an entry. It also returns the
// pending entries, one line each.
func invariantProblems(root string, required []string) (problems, pending []string, err error) {
	raw, err := os.ReadFile(filepath.Join(root, "FEATURES.yaml"))
	if err != nil {
		return nil, nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m featureMap
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("FEATURES.yaml: %w", err)
	}
	tests, err := testNames(root)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, inv := range m.Invariants {
		if seen[inv.ID] {
			problems = append(problems, inv.ID+": listed twice")
			continue
		}
		seen[inv.ID] = true
		switch {
		case len(inv.Tests) == 0 && inv.Pending == "":
			problems = append(problems, inv.ID+": names no tests and no pending item")
		case len(inv.Tests) > 0 && inv.Pending != "":
			problems = append(problems, inv.ID+": names tests and a pending item")
		}
		for _, name := range inv.Tests {
			if !tests[name] {
				problems = append(problems, inv.ID+": test "+name+" does not exist")
			}
		}
		if inv.Pending != "" {
			if !backlogItems[inv.Pending] {
				problems = append(problems, inv.ID+": pending on "+inv.Pending+", which is not a backlog item")
			}
			pending = append(pending, fmt.Sprintf("%s (%s): %s", inv.ID, inv.Pending, inv.Statement))
		}
	}
	for _, id := range required {
		covered := false
		for got := range seen {
			if got == id || strings.HasPrefix(got, id+"-") {
				covered = true
				break
			}
		}
		if !covered {
			problems = append(problems, id+": no entry in the invariants checklist")
		}
	}
	return problems, pending, nil
}

// testNames is every Test function declared in a _test.go file under root,
// leaving out fixture modules under testdata.
func testNames(root string) (map[string]bool, error) {
	names := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				names[fn.Name.Name] = true
			}
		}
		return nil
	})
	return names, err
}
