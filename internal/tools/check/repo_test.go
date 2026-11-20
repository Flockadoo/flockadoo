package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Two things a rename can break without any test failing (the rebrand plan,
// "Things a rename breaks without any test failing"). Both hold before the
// rename as well; they are here so that they still hold after it.

var repoRoot = filepath.Join("..", "..", "..")

func modulePath(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if m, ok := strings.CutPrefix(l, "module "); ok {
			return strings.TrimSpace(m)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

var versionFlag = regexp.MustCompile(`-X '?([^ '=]+)\.(Version|Commit|Date|BuildTime)=`)

// The release and build flags set the version in the package that has it. A
// stale path builds a binary with no version and no error.
func TestVersionFlagsNameTheVersionPackage(t *testing.T) {
	want := modulePath(t) + "/pkg/version"
	seen := 0
	for _, f := range []string{".github/workflows/release.yml", "scripts/build.sh", "scripts/build.ps1", "Dockerfile"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, f))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range versionFlag.FindAllStringSubmatch(string(raw), -1) {
			seen++
			if m[1] != want {
				t.Errorf("%s sets %s.%s; the version package is %s", f, m[1], m[2], want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no -X version flag found; the test reads the wrong files")
	}
}

// Every path CODEOWNERS names exists. A renamed directory would otherwise
// drop out of required review.
func TestCodeownersPathsExist(t *testing.T) {
	f, err := os.Open(filepath.Join(repoRoot, ".github", "CODEOWNERS"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		n++
		p := strings.TrimSuffix(strings.TrimPrefix(fields[0], "/"), "/")
		if strings.ContainsAny(p, "*?[") {
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(p))); err != nil {
			t.Errorf("CODEOWNERS names %s, which does not exist", fields[0])
		}
	}
	if n == 0 {
		t.Fatal("CODEOWNERS lists no paths")
	}
}
