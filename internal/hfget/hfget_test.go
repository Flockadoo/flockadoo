package hfget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C1. The client downloads a model with the hf CLI
// at the commit the catalog pins, into the standard Hugging Face cache, and
// leaves out the folders the verifier never measures. The hf CLI runs from
// the client's own venv, built by uv from requirements pinned by hash.

const sha = "5617a9f61b028005a4858fdac845db406aefb181"

// call is one command a fake runner received.
type call struct {
	name string
	args []string
}

// recorder is a fake runner: it records each call and runs do, if set.
type recorder struct {
	calls []call
	do    func(c call, stdout, stderr io.Writer) error
}

func (r *recorder) run(_ context.Context, name string, args []string, stdout, stderr io.Writer) error {
	c := call{name, slices.Clone(args)}
	r.calls = append(r.calls, c)
	if r.do != nil {
		return r.do(c, stdout, stderr)
	}
	return nil
}

func TestADownloadIsPinnedToTheCommitAndLeavesOutUnmeasuredFolders(t *testing.T) {
	r := &recorder{}
	d := Downloader{HF: "/venv/bin/hf", Run: r.run, Out: io.Discard}
	if err := d.Download(t.Context(), "baai/bge-m3", sha, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0].name != "/venv/bin/hf" {
		t.Fatalf("calls %+v, want one call to the venv's hf", r.calls)
	}
	args := r.calls[0].args
	if !slices.Equal(args[:4], []string{"download", "baai/bge-m3", "--revision", sha}) {
		t.Errorf("args %v, want download baai/bge-m3 --revision %s first", args, sha)
	}
	if slices.Contains(args, "--cache-dir") || slices.Contains(args, "--local-dir") {
		t.Errorf("args %v: the download must go to the standard cache, with no --cache-dir or --local-dir", args)
	}
	for _, dir := range verify.SkipManifestDirs() {
		if !containsPair(args, "--exclude", dir+"/*") {
			t.Errorf("args %v do not exclude %s/*", args, dir)
		}
	}
}

func TestAGGUFDownloadIncludesOnlyItsFile(t *testing.T) {
	r := &recorder{}
	d := Downloader{HF: "hf", Run: r.run, Out: io.Discard}
	if err := d.Download(t.Context(), "unsloth/Qwen3.8-27B-GGUF", sha, []string{"BF16/*"}); err != nil {
		t.Fatal(err)
	}
	if !containsPair(r.calls[0].args, "--include", "BF16/*") {
		t.Errorf("args %v do not include BF16/*", r.calls[0].args)
	}
}

// Without a full commit nothing runs: a branch would download whatever it
// points at today (as download.Verified makes no request without a hash).
func TestNothingIsDownloadedWithoutAFullCommit(t *testing.T) {
	for _, bad := range []string{"", "main", sha[:12], strings.ToUpper(sha), sha + "0"} {
		r := &recorder{}
		d := Downloader{HF: "hf", Run: r.run, Out: io.Discard}
		err := d.Download(t.Context(), "baai/bge-m3", bad, nil)
		if !errors.Is(err, ErrNotFullCommit) {
			t.Errorf("revision %q: err %v, want ErrNotFullCommit", bad, err)
		}
		if len(r.calls) != 0 {
			t.Errorf("revision %q: ran %+v", bad, r.calls)
		}
	}
}

// A gated repo says what to do: accept the licence on its page, then log in.
func TestAGatedRepoSaysHowToGetAccess(t *testing.T) {
	r := &recorder{do: func(_ call, _, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "huggingface_hub.errors.GatedRepoError: 401 Client Error. Cannot access gated repo\n")
		return errors.New("exit status 1")
	}}
	d := Downloader{HF: "hf", Run: r.run, Out: io.Discard}
	err := d.Download(t.Context(), "meta-llama/Llama-3.1-8B-Instruct", sha, nil)
	if !errors.Is(err, ErrGated) {
		t.Fatalf("err %v, want ErrGated", err)
	}
	for _, want := range []string{"https://huggingface.co/meta-llama/Llama-3.1-8B-Instruct", "flockadoo model login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not say %q", err, want)
		}
	}
}

// The Hugging Face token reaches hf only through the environment it inherits,
// never as an argument (I12).
func TestTheTokenIsNeverAnArgument(t *testing.T) {
	const token = "hf_secretTOKEN123"
	t.Setenv("HF_TOKEN", token)
	r := &recorder{}
	var out bytes.Buffer
	d := Downloader{HF: "hf", Run: r.run, Out: &out}
	if err := d.Download(t.Context(), "baai/bge-m3", sha, nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.calls[0].args {
		if strings.Contains(a, token) {
			t.Errorf("the token is in the arguments: %v", r.calls[0].args)
		}
	}
	if strings.Contains(out.String(), token) {
		t.Errorf("the token is in the output: %q", out.String())
	}
}

// The venv is built once from the embedded requirements, installed with
// --require-hashes, and rebuilt only when the pinned requirements change.
func TestTheVenvIsBuiltFromThePinnedRequirements(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "venv-hf")
	var installed []byte
	r := &recorder{do: func(c call, _, _ io.Writer) error {
		switch {
		case len(c.args) > 0 && c.args[0] == "venv":
			return os.MkdirAll(dir, 0o755)
		case len(c.args) > 1 && c.args[0] == "pip" && c.args[1] == "install":
			i := slices.Index(c.args, "-r")
			if i < 0 || !slices.Contains(c.args, "--require-hashes") {
				t.Errorf("pip install %v: want --require-hashes -r <requirements>", c.args)
				return nil
			}
			var err error
			if installed, err = os.ReadFile(c.args[i+1]); err != nil {
				return err
			}
			hf := venvExe(dir, "hf")
			if err := os.MkdirAll(filepath.Dir(hf), 0o755); err != nil {
				return err
			}
			return os.WriteFile(hf, nil, 0o755)
		}
		return nil
	}}
	v := Venv{Dir: dir, UV: "uv", Run: r.run, Out: io.Discard}

	hf, err := v.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if hf != venvExe(dir, "hf") {
		t.Errorf("hf %s, want %s", hf, venvExe(dir, "hf"))
	}
	if !bytes.Equal(installed, requirements) || !bytes.Contains(installed, []byte("--hash=sha256:")) {
		t.Errorf("installed requirements are not the embedded, hash-pinned ones:\n%s", installed)
	}
	if n := len(r.calls); n != 2 || r.calls[0].name != "uv" {
		t.Fatalf("first build: calls %+v, want uv venv then uv pip install", r.calls)
	}

	r.calls = nil
	if _, err := v.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("an up-to-date venv was rebuilt: %+v", r.calls)
	}

	if err := os.WriteFile(filepath.Join(dir, markerFile), []byte("an older pin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Errorf("a venv with another pin was not rebuilt: %+v", r.calls)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
