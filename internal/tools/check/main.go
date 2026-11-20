// Command check runs every gate CI runs, in order, and stops at the first
// failure. Run it before reporting a change as done:
//
//	go run ./internal/tools/check
//
// It is written in Go so it runs unchanged on Linux, macOS and Windows, and CI
// runs exactly this on each (plan item A1, section 19.D).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

func main() {
	lintBase := flag.String("lint-base", "",
		"lint only what changed since the merge base with this revision; empty, the default, lints the whole codebase (plan item L1)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mainErr(ctx, *lintBase); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "check failed:", err)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}

func mainErr(ctx context.Context, lintBase string) error {
	out, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return fmt.Errorf("run this inside the flockadoo module")
	}
	root := filepath.Dir(gomod)
	golangci, err := findGolangciLint(ctx)
	if err != nil {
		return err
	}
	return run(ctx, os.Stdout, options{
		dir:        root,
		lintConfig: filepath.Join(root, ".golangci.yml"),
		lintBase:   lintBase,
		golangci:   golangci,

		requiredInvariants: requiredInvariants,
	})
}
