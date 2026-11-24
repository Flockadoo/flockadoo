// Package hfget downloads a model from Hugging Face at a pinned commit with
// the hf CLI, into the standard Hugging Face cache (the model revisions plan
// in flockadoo-plans, item C1). The hf CLI runs from the client's own venv,
// ~/.venv-hf, built by uv from requirements pinned by hash.
package hfget

import (
	"context"
	_ "embed"
	"errors"
	"io"
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

var errNotImplemented = errors.New("hfget: not implemented")

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
	return errNotImplemented
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
	return "", errNotImplemented
}
