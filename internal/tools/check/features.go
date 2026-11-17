package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// featureMap is FEATURES.yaml: what each feature is, where it lives and what
// tests it (plan item G1, section 19.B).
type featureMap struct {
	Features []feature `yaml:"features"`
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
	if len(problems) > 0 {
		_, _ = fmt.Fprintf(w, "update FEATURES.yaml with the code:\n%s\n", strings.Join(problems, "\n"))
		return fmt.Errorf("%d problems in FEATURES.yaml", len(problems))
	}
	return nil
}
