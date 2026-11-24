// Package hfget downloads a model from Hugging Face at a pinned commit with
// the hf CLI, into the standard Hugging Face cache (the model revisions plan
// in flockadoo-plans, item C1). The hf CLI runs from the client's own venv,
// ~/.venv-hf, built by uv from requirements pinned by hash.
package hfget

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// ErrNotFullCommit is a download asked for without a full commit SHA.
var ErrNotFullCommit = errors.New("a model is downloaded only at a full 40-character commit SHA")

// ErrGated is a repo whose licence the operator has not accepted, or a
// download without a Hugging Face login.
var ErrGated = errors.New("the model's repo is gated")

// markerFile, in the venv, holds the SHA-256 of the requirements it was built from.
const markerFile = ".flockadoo-requirements-sha256"

// requirements is huggingface_hub and its dependencies, pinned by hash
// (uv pip compile --universal --generate-hashes requirements-hf.in).
//
//go:embed requirements-hf.txt
var requirements []byte

// Runner runs a program to completion with its output going to stdout and
// stderr. It inherits the client's environment, so HF_TOKEN reaches hf.
type Runner func(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ExecRunner runs a program with exec.CommandContext. It sets no environment,
// so the program inherits the client's, HF_TOKEN included.
func ExecRunner(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is the client's own uv or the hf in its own venv, and args are built here, not taken from input
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// Downloader runs hf download.
type Downloader struct {
	HF  string    // the hf program
	Run Runner    // runs it
	Out io.Writer // hf's progress
}

// Download fetches repo at commit sha into the Hugging Face cache, leaving out
// the folders the verifier never measures. include limits it to matching
// files (a GGUF and its shards); empty, the whole commit.
func (d Downloader) Download(ctx context.Context, repo, sha string, include []string) error {
	if !fullCommit.MatchString(sha) {
		return fmt.Errorf("%w: %s at %q", ErrNotFullCommit, repo, sha)
	}
	args := []string{"download", repo, "--revision", sha}
	for _, dir := range verify.SkipManifestDirs() {
		args = append(args, "--exclude", dir+"/*")
	}
	for _, pattern := range include {
		args = append(args, "--include", pattern)
	}
	out := d.Out
	if out == nil {
		out = io.Discard
	}
	var stderr bytes.Buffer
	if err := d.Run(ctx, d.HF, args, out, io.MultiWriter(out, &stderr)); err != nil {
		if gated(stderr.String()) {
			return fmt.Errorf("%w: %s needs its licence accepted at https://huggingface.co/%s and a Hugging Face login (run `flockadoo model login`), then try again: %w",
				ErrGated, repo, repo, err)
		}
		return fmt.Errorf("hf download %s@%s: %w", repo, sha, err)
	}
	return nil
}

// gated reports whether hf's error output is an access refusal.
func gated(stderr string) bool {
	for _, s := range []string{"GatedRepoError", "Cannot access gated repo", "401 Client Error", "403 Client Error"} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// Venv is the client's own venv for hf.
type Venv struct {
	Dir string    // ~/.venv-hf
	UV  string    // the uv program
	Run Runner    // runs it
	Out io.Writer // uv's output
}

// Ensure builds the venv from the pinned requirements unless it already was,
// and returns the path of its hf.
func (v Venv) Ensure(ctx context.Context) (string, error) {
	hf := venvExe(v.Dir, "hf")
	sum := sha256.Sum256(requirements)
	want := hex.EncodeToString(sum[:])
	if got, err := os.ReadFile(filepath.Join(v.Dir, markerFile)); err == nil && strings.TrimSpace(string(got)) == want {
		if _, err := os.Stat(hf); err == nil {
			return hf, nil
		}
	}
	out := v.Out
	if out == nil {
		out = io.Discard
	}
	req, err := os.CreateTemp("", "flockadoo-requirements-hf-*.txt")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(req.Name()) }()
	if _, err := req.Write(requirements); err != nil {
		_ = req.Close()
		return "", err
	}
	if err := req.Close(); err != nil {
		return "", err
	}
	// --managed-python: uv's own Python, checked against the hashes that uv
	// release carries, not whatever the distribution ships. --clear replaces a
	// venv built from another pin.
	if err := v.Run(ctx, v.UV, []string{"venv", "--python", "3.12", "--managed-python", "--clear", v.Dir}, out, out); err != nil {
		return "", fmt.Errorf("create %s: %w", v.Dir, err)
	}
	if err := v.Run(ctx, v.UV, []string{"pip", "install", "--python", venvExe(v.Dir, "python"), "--require-hashes", "-r", req.Name()}, out, out); err != nil {
		return "", fmt.Errorf("install hf into %s: %w", v.Dir, err)
	}
	if err := os.WriteFile(filepath.Join(v.Dir, markerFile), []byte(want+"\n"), 0o644); err != nil {
		return "", err
	}
	return hf, nil
}
