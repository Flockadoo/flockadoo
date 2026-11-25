package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Model revisions plan, "Keeping the pins current" (U1b). `go run
// ./internal/tools/pins -update` moves the pins for a pull request:
//   - uv.json to the latest uv release (or -uv-version), each archive's SHA-256
//     taken only when GitHub's asset digest and astral's own .sha256 file agree;
//   - llamacpp.json to GitHub's digests for its tag, which moves only with
//     -llama-tag, a deliberate choice;
//   - requirements-hf.txt by re-running uv pip compile --upgrade.
//
// Which files are pinned is read from the pin files themselves, so the list
// lives in one place. Anything missing or disagreeing stops the update before
// any file is written.

func hexOf(c byte) string { return strings.Repeat(string(c), 64) }

// fakeGitHub serves the release API and the download site for uv and llama.cpp.
type fakeGitHub struct {
	uvTag     string
	uvSums    map[string]string // asset -> digest
	uvFiles   map[string]string // asset -> what its .sha256 file says
	llamaRels map[string]map[string]string
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	release := func(w http.ResponseWriter, tag string, sums map[string]string) {
		var assets []map[string]any
		for name, sum := range sums {
			assets = append(assets, map[string]any{"name": name, "digest": "sha256:" + sum})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api/repos/astral-sh/uv/releases/latest" || p == "/api/repos/astral-sh/uv/releases/tags/"+f.uvTag:
			release(w, f.uvTag, f.uvSums)
		case strings.HasPrefix(p, "/api/repos/ggml-org/llama.cpp/releases/tags/"):
			tag := strings.TrimPrefix(p, "/api/repos/ggml-org/llama.cpp/releases/tags/")
			sums, ok := f.llamaRels[tag]
			if !ok {
				http.NotFound(w, r)
				return
			}
			release(w, tag, sums)
		case strings.HasPrefix(p, "/dl/astral-sh/uv/releases/download/"+f.uvTag+"/") && strings.HasSuffix(p, ".sha256"):
			name := strings.TrimSuffix(filepath.Base(p), ".sha256")
			sum, ok := f.uvFiles[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", sum, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var uvNames = []string{
	"uv-x86_64-unknown-linux-gnu.tar.gz", "uv-aarch64-unknown-linux-gnu.tar.gz",
	"uv-aarch64-apple-darwin.tar.gz", "uv-x86_64-pc-windows-msvc.zip",
}

func newFake() *fakeGitHub {
	f := &fakeGitHub{uvTag: "0.14.0", uvSums: map[string]string{}, uvFiles: map[string]string{}, llamaRels: map[string]map[string]string{}}
	for i, n := range uvNames {
		sum := hexOf("1234"[i])
		f.uvSums[n], f.uvFiles[n] = sum, sum
	}
	f.llamaRels["b11770"] = map[string]string{
		"llama-b11770-bin-win-cuda-12.4-x64.zip": hexOf('a'),
		"cudart-llama-bin-win-cuda-12.4-x64.zip": hexOf('b'),
		"llama-b11770-bin-macos-arm64.tar.gz":    hexOf('c'),
	}
	f.llamaRels["b12000"] = map[string]string{
		"llama-b12000-bin-win-cuda-12.4-x64.zip": hexOf('d'),
		"cudart-llama-bin-win-cuda-12.4-x64.zip": hexOf('e'),
		"llama-b12000-bin-macos-arm64.tar.gz":    hexOf('f'),
	}
	return f
}

// repoCopy is a repository root holding copies of today's pin files.
func repoCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"pkg/engine/uv.json", "pkg/engine/llamacpp.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type pinFile struct {
	Version string            `json:"version"`
	Tag     string            `json:"tag"`
	SHA256  map[string]string `json:"sha256"`
}

func readPin(t *testing.T, root, rel string) pinFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	var p pinFile
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(srv *httptest.Server, root string, compiled *[]string) options {
	return options{
		Root: root, API: srv.URL + "/api", Download: srv.URL + "/dl", Client: srv.Client(), Out: io.Discard,
		Compile: func(_ context.Context, dir string) error { *compiled = append(*compiled, dir); return nil },
	}
}

func TestUpdateMovesEveryPinFromAgreeingSources(t *testing.T) {
	f := newFake()
	srv := f.server(t)
	root := repoCopy(t)
	var compiled []string
	if err := update(t.Context(), opts(srv, root, &compiled)); err != nil {
		t.Fatal(err)
	}
	uv := readPin(t, root, "pkg/engine/uv.json")
	if uv.Version != "0.14.0" || len(uv.SHA256) != 4 {
		t.Errorf("uv.json = %+v, want 0.14.0 with four archives", uv)
	}
	for _, n := range uvNames {
		if uv.SHA256[n] != f.uvSums[n] {
			t.Errorf("uv.json %s = %s, want %s", n, uv.SHA256[n], f.uvSums[n])
		}
	}
	llama := readPin(t, root, "pkg/engine/llamacpp.json")
	if llama.Tag != "b11770" {
		t.Errorf("llamacpp.json tag %q, want it kept at b11770", llama.Tag)
	}
	for n, sum := range f.llamaRels["b11770"] {
		if llama.SHA256[n] != sum {
			t.Errorf("llamacpp.json %s = %s, want %s", n, llama.SHA256[n], sum)
		}
	}
	if len(compiled) != 1 || compiled[0] != root {
		t.Errorf("compile ran %v, want once in %s", compiled, root)
	}
}

func TestMovingTheLlamaTagRenamesItsArchives(t *testing.T) {
	f := newFake()
	srv := f.server(t)
	root := repoCopy(t)
	var compiled []string
	o := opts(srv, root, &compiled)
	o.LlamaTag = "b12000"
	if err := update(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	llama := readPin(t, root, "pkg/engine/llamacpp.json")
	want := f.llamaRels["b12000"]
	if llama.Tag != "b12000" || len(llama.SHA256) != len(want) {
		t.Fatalf("llamacpp.json = %+v, want b12000's three archives", llama)
	}
	for n, sum := range want {
		if llama.SHA256[n] != sum {
			t.Errorf("%s = %q, want %s", n, llama.SHA256[n], sum)
		}
	}
}

// A published checksum that disagrees with GitHub's digest, or an archive
// the release lacks, stops the update: no pin file changes and nothing is
// compiled.
func TestADisagreementOrAGapChangesNothing(t *testing.T) {
	for name, spoil := range map[string]func(f *fakeGitHub){
		"uv .sha256 disagrees":      func(f *fakeGitHub) { f.uvFiles[uvNames[2]] = hexOf('9') },
		"uv archive missing":        func(f *fakeGitHub) { delete(f.uvSums, uvNames[3]) },
		"llama archive missing":     func(f *fakeGitHub) { delete(f.llamaRels["b11770"], "cudart-llama-bin-win-cuda-12.4-x64.zip") },
		"llama digest not a sha256": func(f *fakeGitHub) { f.llamaRels["b11770"]["llama-b11770-bin-macos-arm64.tar.gz"] = "abc" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			spoil(f)
			srv := f.server(t)
			root := repoCopy(t)
			before := map[string][]byte{}
			for _, rel := range []string{"pkg/engine/uv.json", "pkg/engine/llamacpp.json"} {
				before[rel], _ = os.ReadFile(filepath.Join(root, rel))
			}
			var compiled []string
			if err := update(t.Context(), opts(srv, root, &compiled)); err == nil {
				t.Error("no error")
			}
			for rel, data := range before {
				if after, _ := os.ReadFile(filepath.Join(root, rel)); !bytes.Equal(after, data) {
					t.Errorf("%s changed", rel)
				}
			}
			if len(compiled) != 0 {
				t.Errorf("compiled after a refusal")
			}
		})
	}
}
