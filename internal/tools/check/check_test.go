package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Plan item A1: the check command rejects each fixture module in testdata and
// accepts the clean one. The fixtures are copied to a temporary directory, so
// nothing a tool writes lands in the repository.

func TestRejectsAnUnformattedFile(t *testing.T) {
	out, err := runFixture(t, "unformatted", stepFormat)
	if err == nil {
		t.Fatalf("an unformatted file was accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "bad.go") {
		t.Fatalf("the output does not name the unformatted file:\n%s", out)
	}
}

func TestRejectsAnUntidyGoMod(t *testing.T) {
	out, err := runFixture(t, "untidy", stepTidy)
	if err == nil {
		t.Fatalf("a go.mod that marks a direct dependency indirect was accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "// indirect") {
		t.Fatalf("the output does not show the go.mod diff:\n%s", out)
	}
}

// The rebrand plan, stage 4: a retired name in a file or in a path fails the
// check, an allowed line passes, and an entry that allows nothing fails too,
// so the list only shrinks. The clean fixture has none.
func TestRejectsRetiredNames(t *testing.T) {
	out, err := runFixture(t, "old-names", stepNames)
	if err == nil {
		t.Fatalf("retired names were accepted; output:\n%s", out)
	}
	for _, want := range []string{
		"README.md:2: the old brand",
		"(the path): the old word for a flock",
		"allows nothing any more; remove it: gone.txt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md:3") {
		t.Errorf("an allowed line was reported:\n%s", out)
	}
}

func TestRejectsAForbiddenImport(t *testing.T) {
	out, err := runFixture(t, "forbidden-import", stepLint)
	if err == nil {
		t.Fatalf("production code importing internal/testenv was accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "testenv-only-in-tests") {
		t.Fatalf("the output does not name the broken depguard rule:\n%s", out)
	}
}

// Section 19.D: "it passed locally" and "it passed in CI" mean the same thing.
// Lint judges only what changed since the merge base, and what changed
// depends on git's diff algorithm: with diff.algorithm=histogram in the
// developer's git config, an unchanged function next to a new one stayed
// unchanged, and CI (git's default, myers) judged it new and failed. The
// fixture is that change. Whatever the developer's setting, lint reports the
// new forbidden line and nothing in the function that did not change.
func TestLintJudgesTheSameLinesWhateverTheDiffAlgorithm(t *testing.T) {
	for _, algorithm := range []string{"myers", "histogram", "patience", "minimal"} {
		t.Run(algorithm, func(t *testing.T) {
			// The long form of the path: on Windows the temporary directory
			// can be an 8.3 short name (RUNNER~1), and git reports the long
			// one, so lint matched no changed file (0 issues in CI).
			dir, err := filepath.EvalSymlinks(copyFixture(t, "diff-algorithm"))
			if err != nil {
				t.Fatal(err)
			}
			// The configuration sits in the module root, as in this
			// repository: golangci-lint makes paths relative to it, and on
			// Windows the temporary directory can be on another drive
			// than the checkout, where no relative path exists.
			repoCfg, err := os.ReadFile(filepath.Join("..", "..", "..", ".golangci.yml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), repoCfg, 0o600); err != nil {
				t.Fatal(err)
			}
			gitconfig := filepath.Join(t.TempDir(), "gitconfig")
			cfg := "[diff]\n\talgorithm = " + algorithm + "\n[user]\n\tname = check\n\temail = check@example.invalid\n[init]\n\tdefaultBranch = main\n"
			if err := os.WriteFile(gitconfig, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{"GIT_CONFIG_GLOBAL=" + gitconfig, "GIT_CONFIG_NOSYSTEM=1"}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.CommandContext(context.Background(), "git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), env...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			pkg := filepath.Join(dir, "pkg", "x")
			base, err := os.ReadFile(filepath.Join(pkg, "sudo.go.base"))
			if err != nil {
				t.Fatal(err)
			}
			head, err := os.ReadFile(filepath.Join(pkg, "sudo.go.head"))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sudo.go.base", "sudo.go.head"} {
				if err := os.Remove(filepath.Join(pkg, name)); err != nil {
					t.Fatal(err)
				}
			}
			sudo := filepath.Join(pkg, "sudo.go")
			if err := os.WriteFile(sudo, base, 0o600); err != nil {
				t.Fatal(err)
			}
			git("init", "-q")
			git("add", "-A")
			git("commit", "-qm", "base")
			git("tag", "base")
			if err := os.WriteFile(sudo, head, 0o600); err != nil {
				t.Fatal(err)
			}
			git("commit", "-qam", "head")

			o := fixtureOptions(t, dir, stepLint)
			o.lintConfig = filepath.Join(dir, ".golangci.yml")
			o.lintBase = "base"
			o.env = append(o.env, env...)
			var buf bytes.Buffer
			err = run(context.Background(), &buf, o)
			out := buf.String()
			if err == nil || !strings.Contains(out, "fmt.Println") {
				top, _ := exec.CommandContext(context.Background(), "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
				t.Fatalf("the new fmt.Println was not reported (err %v); module %s, git top level %s\n%s", err, dir, strings.TrimSpace(string(top)), out)
			}
			if strings.Contains(out, "os.Stdout") || strings.Contains(out, "os.Stderr") || strings.Contains(out, "G204") {
				t.Fatalf("with diff.algorithm=%s, lint judged runSudo, which did not change:\n%s", algorithm, out)
			}
		})
	}
}

// The positive control: every step accepts a module with nothing wrong, so the
// rejections above are about the fixture and not about a step that always fails.
func TestAcceptsACleanModule(t *testing.T) {
	out, err := runFixture(t, "clean")
	if err != nil {
		t.Fatalf("the clean module was rejected: %v\n%s", err, out)
	}
	for _, name := range allStepNames() {
		if !strings.Contains(out, name) {
			t.Errorf("step %q did not run on the clean module:\n%s", name, out)
		}
	}
}

// Section 19.D: the command stops at the first failure, so the first problem
// is the one on screen.
func TestStopsAtTheFirstFailure(t *testing.T) {
	out, err := runFixture(t, "unformatted")
	if err == nil {
		t.Fatalf("the unformatted module passed every step:\n%s", out)
	}
	if !strings.Contains(out, stepFormat) {
		t.Fatalf("the formatting step did not run:\n%s", out)
	}
	if strings.Contains(out, stepTidy) {
		t.Fatalf("a step after the failing one still ran:\n%s", out)
	}
}

// Section 19.D: the tests run with HOME and the per-user directories pointed
// at an empty temporary directory, never at the developer's own.
func TestRunsTheTestsWithATemporaryHome(t *testing.T) {
	dir := copyFixture(t, "clean")
	probe := `package x

import (
	"os"
	"testing"
)

func TestReportHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("HOME=%s", home)
}
`
	if err := os.WriteFile(filepath.Join(dir, "pkg", "x", "home_test.go"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	o := fixtureOptions(t, dir, stepTests)
	o.testFlags = []string{"-v", "-run", "TestReportHome"}
	if err := run(context.Background(), &buf, o); err != nil {
		t.Fatalf("tests step failed: %v\n%s", err, buf.String())
	}
	out := buf.String()
	i := strings.Index(out, "HOME=")
	if i < 0 {
		t.Fatalf("the probe test did not report HOME:\n%s", out)
	}
	home := strings.Fields(out[i+len("HOME="):])[0]
	if home == realHome || strings.HasPrefix(realHome, home) {
		t.Fatalf("tests ran with the developer's home %q", home)
	}
}

func allStepNames() []string {
	return []string{stepFormat, stepTidy, stepVet + " linux/amd64", stepVet + " darwin/arm64", stepVet + " windows/amd64", stepLint, stepTests, stepFeatures, stepNames}
}

// runFixture runs the named steps (all of them when none is named) against a
// copy of testdata/<name>, judged by this repository's lint configuration.
func runFixture(t *testing.T, name string, steps ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := run(context.Background(), &buf, fixtureOptions(t, copyFixture(t, name), steps...))
	return buf.String(), err
}

func fixtureOptions(t *testing.T, dir string, steps ...string) options {
	t.Helper()
	cfg, err := filepath.Abs(filepath.Join("..", "..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lint, err := findGolangciLint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return options{
		dir:        dir,
		lintConfig: cfg,
		golangci:   lint,
		only:       steps,
		// The fixtures need no network and no other toolchain, and nothing a
		// tool writes may land in the home directory of the run.
		env: []string{
			"GOPROXY=off",
			"GOFLAGS=-mod=mod",
			"GOTOOLCHAIN=local",
			"GOLANGCI_LINT_CACHE=" + t.TempDir(),
		},
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// The race detector needs cgo; make the failure say so instead of failing a
// fixture for an unrelated reason.
func TestMain(m *testing.M) {
	if out, err := exec.CommandContext(context.Background(), "go", "env", "CGO_ENABLED").Output(); err == nil && strings.TrimSpace(string(out)) != "1" {
		_, _ = fmt.Fprintln(os.Stderr, "these tests run go test -race, which needs CGO_ENABLED=1 and a C compiler")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Section 19.I: the check fails when a test creates a file in the home
// directory (finding TST-1), and names it.
func TestRejectsATestThatWritesIntoHome(t *testing.T) {
	out, err := runFixture(t, "writes-home", stepTests)
	if err == nil {
		t.Fatalf("a test that wrote into HOME was accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "app.log") {
		t.Fatalf("the output does not name the file the test left in HOME:\n%s", out)
	}
}
