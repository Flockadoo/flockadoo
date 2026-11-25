// Command pins moves the client's download pins for a pull request (model
// revisions plan, "Keeping the pins current"). Run it from the repository
// root, monthly or before a release:
//
//	go run ./internal/tools/pins -update [-uv-version 0.14.0] [-llama-tag b12000]
//
// It rewrites pkg/engine/uv.json (the latest uv unless -uv-version, each
// SHA-256 taken only when GitHub's digest and astral's .sha256 file agree),
// pkg/engine/llamacpp.json (GitHub's digests for the pinned tag, which moves
// only with -llama-tag) and internal/hfget/requirements-hf.txt (uv pip
// compile --upgrade, with the uv on the PATH). Nothing is written unless every
// check passes. A maintainer reviews and merges the pull request; the
// engine-pins workflow checks it.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func main() {
	doUpdate := flag.Bool("update", false, "rewrite the pin files")
	root := flag.String("root", ".", "the repository root")
	uvVersion := flag.String("uv-version", "", "the uv release to pin (default: the latest)")
	llamaTag := flag.String("llama-tag", "", "the llama.cpp tag to pin (default: the one pinned now)")
	flag.Parse()
	if !*doUpdate {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/tools/pins -update [-uv-version X.Y.Z] [-llama-tag bNNNN]")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := update(ctx, options{
		Root: *root, API: "https://api.github.com", Download: "https://github.com",
		Client: &http.Client{Timeout: time.Minute}, UVVersion: *uvVersion, LlamaTag: *llamaTag,
		Compile: compile, Out: os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pins: %v\n", err)
		os.Exit(1)
	}
}

// compile regenerates the hash-pinned hf requirements, upgrading every
// package within requirements-hf.in, as they were made.
func compile(ctx context.Context, root string) error {
	cmd := exec.CommandContext(ctx, "uv", "pip", "compile", "--universal", "--generate-hashes", "--no-header", "--upgrade",
		"internal/hfget/requirements-hf.in", "-o", "internal/hfget/requirements-hf.txt")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
	return cmd.Run()
}
