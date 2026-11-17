package main

import (
	"context"
	"io"
)

// Step names, as printed.
const (
	stepFormat = "formatting"
	stepTidy   = "go.mod is tidy"
	stepVet    = "vet"
	stepLint   = "lint"
	stepTests  = "tests"
)

type options struct {
	dir        string   // module root to check
	lintConfig string   // golangci-lint configuration file
	lintBase   string   // judge only changes since the merge base with this revision; "" judges everything
	golangci   string   // golangci-lint binary
	env        []string // added to every tool's environment
	only       []string // run only these steps, by name; nil runs every step
	testFlags  []string // added to go test
}

// run is not written yet: plan item A1, tests first. It accepts everything,
// so the tests fail because nothing is rejected.
func run(ctx context.Context, w io.Writer, o options) error {
	return nil
}

// findGolangciLint is not written yet either; it finds nothing and reports no error.
func findGolangciLint(ctx context.Context) (string, error) {
	return "", nil
}
