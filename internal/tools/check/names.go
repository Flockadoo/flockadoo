package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// namesAllowFile is where a retired name may stay, relative to the module
// root: a path glob, a tab and a regular expression the line must match.
const namesAllowFile = "internal/tools/check/names_allow.txt"

// retiredNames (the rebrand plan, stages 1 and 4; the names are on
// docs/naming.md in internal-docs): the brand before Flockadoo, the network
// client's word for its network, and the product's old word for what a
// provider runs never come back. Each pattern is written so that this file
// does not match it.
var retiredNames = []struct {
	what string
	re   *regexp.Regexp
}{
	{"the old brand", regexp.MustCompile(`(?i)infer(?:oute|scale|bid)|sent[n]l`)},
	{"the network's old name (say Winglink)", regexp.MustCompile(`(?i)tail[n]et`)},
	{"the old word for a flock", regexp.MustCompile(`(?i)clus[t]er`)},
}

type allowedName struct {
	glob string
	line *regexp.Regexp
	used bool
}

func readAllowedNames(file string) ([]*allowedName, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*allowedName
	for n, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSuffix(l, "\r") // checked out with CRLF on Windows
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		glob, expr, ok := strings.Cut(l, "\t")
		if !ok {
			return nil, fmt.Errorf("%s:%d: want a path glob, a tab and a regular expression", file, n+1)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, n+1, err)
		}
		out = append(out, &allowedName{glob: glob, line: re})
	}
	return out, nil
}

// findRetiredNames returns each retired name in files (slash path to
// content) and in their paths that no entry allows, as "path:line: what", and
// each entry that allowed nothing.
func findRetiredNames(files map[string][]byte, allow []*allowedName) (found, stale []string) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	check := func(file, where, line string) {
		for _, r := range retiredNames {
			if !r.re.MatchString(line) {
				continue
			}
			ok := false
			for _, a := range allow {
				if m, err := path.Match(a.glob, file); err == nil && m && a.line.MatchString(line) {
					a.used, ok = true, true
				}
			}
			if !ok {
				found = append(found, fmt.Sprintf("%s: %s", where, r.what))
			}
		}
	}
	for _, p := range paths {
		check(p, p+" (the path)", p)
		data := files[p]
		if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			continue // binary
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for n := 1; sc.Scan(); n++ {
			check(p, fmt.Sprintf("%s:%d", p, n), sc.Text())
		}
	}
	for _, a := range allow {
		if !a.used {
			stale = append(stale, a.glob+"\t"+a.line.String())
		}
	}
	return found, stale
}

// moduleFiles is every file git tracks under dir, or every file under it
// when dir is not in a git work tree (a fixture's copy).
func moduleFiles(ctx context.Context, dir string) (map[string][]byte, error) {
	var names []string
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z").Output(); err == nil { //nolint:gosec // G204: dir is the module root the check was given
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				names = append(names, p)
			}
		}
	} else {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, err := filepath.Rel(dir, p)
				if err != nil {
					return err
				}
				names = append(names, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	files := map[string][]byte{}
	for _, p := range names {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			return nil, err
		}
		files[p] = data
	}
	return files, nil
}

// checkNames fails on any retired name outside the allowed list, and on an
// entry of the list that allows nothing, so the list only shrinks.
func checkNames(ctx context.Context, o options, w io.Writer) error {
	files, err := moduleFiles(ctx, o.dir)
	if err != nil {
		return err
	}
	allow, err := readAllowedNames(filepath.Join(o.dir, filepath.FromSlash(namesAllowFile)))
	if err != nil {
		return err
	}
	found, stale := findRetiredNames(files, allow)
	for _, f := range found {
		_, _ = fmt.Fprintln(w, f)
	}
	for _, s := range stale {
		_, _ = fmt.Fprintf(w, "%s allows nothing any more; remove it: %s\n", namesAllowFile, s)
	}
	if len(found)+len(stale) > 0 {
		return fmt.Errorf("%d retired names and %d stale entries; use the names on docs/naming.md (internal-docs)", len(found), len(stale))
	}
	return nil
}
