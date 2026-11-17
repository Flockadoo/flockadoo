package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Step names, as printed.
const (
	stepFormat = "formatting"
	stepTidy   = "go.mod is tidy"
	stepVet    = "vet"
	stepLint   = "lint"
	stepTests  = "tests"

	stepFeatures = "features"
)

// golangciVersion is the one version whose findings the configuration is
// written for; another version finds other things.
const golangciVersion = "2.15.0"

type options struct {
	dir        string   // module root to check
	lintConfig string   // golangci-lint configuration file
	lintBase   string   // judge only changes since the merge base with this revision; "" judges everything
	golangci   string   // golangci-lint binary
	env        []string // added to every tool's environment
	only       []string // run only these steps, by name; nil runs every step
	testFlags  []string // added to go test
}

type step struct {
	name string
	run  func(ctx context.Context, o options, w io.Writer) error
}

// vetTargets are the platforms the client ships for. Each one's files are
// vetted from any host, so a darwin or windows break shows up on Linux.
var vetTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

func steps() []step {
	s := []step{
		{stepFormat, checkFormat},
		{stepTidy, func(ctx context.Context, o options, w io.Writer) error {
			return goTool(ctx, o, w, nil, "mod", "tidy", "-diff")
		}},
	}
	for _, t := range vetTargets {
		env := []string{"GOOS=" + t.goos, "GOARCH=" + t.goarch, "CGO_ENABLED=0"}
		s = append(s, step{stepVet + " " + t.goos + "/" + t.goarch, func(ctx context.Context, o options, w io.Writer) error {
			return goTool(ctx, o, w, env, "vet", "./...")
		}})
	}
	return append(s, step{stepLint, lint}, step{stepTests, tests}, step{stepFeatures, checkFeatures})
}

// run runs the steps in order and stops at the first failure (plan section
// 19.D), so the first problem is the one on screen.
func run(ctx context.Context, w io.Writer, o options) error {
	for _, s := range steps() {
		if len(o.only) > 0 && !slices.Contains(o.only, s.name) {
			continue
		}
		start := time.Now()
		var out bytes.Buffer
		err := s.run(ctx, o, &out)
		status := "ok"
		if err != nil {
			status = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "%-20s %-4s %6.1fs\n", s.name, status, time.Since(start).Seconds())
		if text := indent(out.String()); text != "" {
			_, _ = fmt.Fprintln(w, text)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// checkFormat lists every Go file that gofmt would change. Fixture modules
// under testdata are data, not code, and are left alone.
func checkFormat(_ context.Context, o options, w io.Writer) error {
	var bad []string
	err := filepath.WalkDir(o.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != o.dir && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		formatted, err := format.Source(src)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !bytes.Equal(src, formatted) {
			rel, _ := filepath.Rel(o.dir, path)
			bad = append(bad, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		_, _ = fmt.Fprintf(w, "run gofmt -w on:\n%s\n", strings.Join(bad, "\n"))
		return fmt.Errorf("%d unformatted files", len(bad))
	}
	return nil
}

func lint(ctx context.Context, o options, w io.Writer) error {
	if o.golangci == "" {
		return errors.New("no golangci-lint binary")
	}
	args := []string{"run", "--config", o.lintConfig}
	if o.lintBase != "" {
		if err := tool(ctx, o, io.Discard, nil, "git", "rev-parse", "--verify", "--quiet", o.lintBase+"^{commit}"); err != nil {
			return fmt.Errorf("lint base %q is not a commit here; fetch it (CI: actions/checkout with fetch-depth: 0) or pass -lint-base", o.lintBase)
		}
		args = append(args, "--new-from-merge-base="+o.lintBase)
	}
	return tool(ctx, o, w, nil, o.golangci, append(args, "./...")...)
}

// tests runs the race-enabled suite with HOME and every per-user directory
// pointed at a new, empty temporary directory, so no test can read or write
// the developer's own files (finding TST-1).
func tests(ctx context.Context, o options, w io.Writer) error {
	home, err := os.MkdirTemp("", "inferoute-check-home-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env, err := isolatedHome(ctx, o, home)
	if err != nil {
		return err
	}
	args := append([]string{"test", "-race", "-count=1", "-shuffle=on"}, o.testFlags...)
	return goTool(ctx, o, w, env, append(args, "./...")...)
}

// isolatedHome returns the environment that moves every per-user directory
// into home. Go's own caches and settings stay where they are (they are
// resolved first and pinned), so the run does not rebuild everything.
func isolatedHome(ctx context.Context, o options, home string) ([]string, error) {
	var pinned struct{ GOCACHE, GOMODCACHE, GOPATH, GOENV string }
	var out bytes.Buffer
	if err := goTool(ctx, o, &out, nil, "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"); err != nil {
		return nil, fmt.Errorf("go env: %w: %s", err, out.String())
	}
	if err := json.Unmarshal(out.Bytes(), &pinned); err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	return []string{
		"GOCACHE=" + pinned.GOCACHE,
		"GOMODCACHE=" + pinned.GOMODCACHE,
		"GOPATH=" + pinned.GOPATH,
		"GOENV=" + pinned.GOENV,
		"HOME=" + home,
		"USERPROFILE=" + home,
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"HF_HUB_CACHE=" + filepath.Join(home, ".cache", "huggingface", "hub"),
		"HUGGINGFACE_HUB_CACHE=" + filepath.Join(home, ".cache", "huggingface", "hub"),
	}, nil
}

func goTool(ctx context.Context, o options, w io.Writer, env []string, args ...string) error {
	return tool(ctx, o, w, env, "go", args...)
}

// tool runs name in the module root. Later environment entries win, so env
// overrides o.env, which overrides the caller's environment.
func tool(ctx context.Context, o options, w io.Writer, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the check runs the toolchain and golangci-lint by design
	cmd.Dir = o.dir
	cmd.Env = append(append(os.Environ(), o.env...), env...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// findGolangciLint finds golangci-lint on PATH or in GOPATH/bin, and refuses
// any version but the one the configuration is written for.
func findGolangciLint(ctx context.Context) (string, error) {
	install := "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v" + golangciVersion
	path, err := exec.LookPath("golangci-lint")
	if err != nil {
		out, gerr := exec.CommandContext(ctx, "go", "env", "GOPATH").Output()
		if gerr != nil {
			return "", fmt.Errorf("golangci-lint is not on PATH and go env GOPATH failed: %w", gerr)
		}
		candidate := filepath.Join(strings.TrimSpace(string(out)), "bin", "golangci-lint")
		if path, err = exec.LookPath(candidate); err != nil {
			return "", fmt.Errorf("golangci-lint is not on PATH or in GOPATH/bin; install it: %s", install)
		}
	}
	out, err := exec.CommandContext(ctx, path, "version", "--short").Output() //nolint:gosec // G204: path is the golangci-lint binary just found
	if err != nil {
		return "", fmt.Errorf("%s version: %w", path, err)
	}
	if v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v"); v != golangciVersion {
		return "", fmt.Errorf("%s is golangci-lint %s; this repository needs %s: %s", path, v, golangciVersion, install)
	}
	return path, nil
}

func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
