package fakeengine

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CorpusDir is the copy of flockadoo-server's stream corpus: real engine
// output recorded with the node's scripts/record_sse.sh, copied here by
// scripts/copy_corpus.sh with a SHA256SUMS file. Never edit it by hand.
func CorpusDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "corpus")
}

func loadRecording(recording string) (body []byte, contentType string, err error) {
	base := filepath.Join(CorpusDir(), filepath.FromSlash(recording))
	body, err = os.ReadFile(base + ".sse")
	if err != nil {
		return nil, "", fmt.Errorf("recording %q: %w", recording, err)
	}
	headers, err := os.ReadFile(base + ".headers")
	if err != nil {
		return nil, "", fmt.Errorf("recording %q: %w", recording, err)
	}
	return body, contentTypeOf(headers), nil
}

// VerifyCorpus checks every file in the corpus against SHA256SUMS, and that
// no file is missing from it: an edited, added or removed recording fails.
func VerifyCorpus() error {
	dir := CorpusDir()
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			return fmt.Errorf("SHA256SUMS: bad line %q", sc.Text())
		}
		listed[name] = sum
	}
	var problems []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "SHA256SUMS" || rel == "SOURCE" {
			return nil
		}
		want, ok := listed[rel]
		if !ok {
			problems = append(problems, rel+": not in SHA256SUMS")
			return nil
		}
		delete(listed, rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != want {
			problems = append(problems, rel+": does not match its checksum")
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name := range listed {
		problems = append(problems, name+": listed in SHA256SUMS but missing")
	}
	if len(problems) > 0 {
		return fmt.Errorf("the corpus differs from what scripts/copy_corpus.sh copied:\n%s", strings.Join(problems, "\n"))
	}
	return nil
}
