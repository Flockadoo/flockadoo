package engine

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	dl "github.com/Flockadoo/flockadoo/internal/download"
)

// Model revisions plan, item L1. The llama.cpp release the client installs
// was pinned by tag only and downloaded unchecked, then unpacked and run: a
// changed or replaced archive would have been run as it was. Now each archive
// has a SHA-256 built into the client (llamacpp.json, from GitHub's published
// asset digests) and is fetched with download.Verified. On Windows both
// archives are checked before either is unpacked, so a bad one leaves nothing
// half installed.

// llamaZip is an archive holding the named files at its top.
func llamaZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// llamaRelease serves archives at /<tag>/<name> and records the paths asked for.
func llamaRelease(t *testing.T, tag string, archives map[string][]byte) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		body, ok := archives[strings.TrimPrefix(r.URL.Path, "/"+tag+"/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

const (
	winLlama  = "llama-b1-bin-win-cuda-12.4-x64.zip"
	winCudart = "cudart-llama-bin-win-cuda-12.4-x64.zip"
)

func windowsLlama(t *testing.T) (map[string][]byte, map[string]string) {
	archives := map[string][]byte{
		winLlama:  llamaZip(t, "llama-server.exe", "ggml.dll"),
		winCudart: llamaZip(t, "cudart64_12.dll"),
	}
	sums := map[string]string{winLlama: sha256Hex(archives[winLlama]), winCudart: sha256Hex(archives[winCudart])}
	return archives, sums
}

func TestLlamaCppIsInstalledFromArchivesThatMatchTheirPins(t *testing.T) {
	archives, sums := windowsLlama(t)
	srv, asked := llamaRelease(t, "b1", archives)
	dir := t.TempDir()
	src := llamaSource{BaseURL: srv.URL, Pin: llamaPin{Tag: "b1", SHA256: sums}, GOOS: "windows", GOARCH: "amd64", Client: srv.Client()}
	if err := installLlamaWith(t.Context(), io.Discard, dir, src); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"llama-server.exe", "ggml.dll", "cudart64_12.dll"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != name {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if len(asked()) != 2 {
		t.Errorf("asked for %v, want the two pinned archives", asked())
	}
}

// One archive that does not match its pin installs nothing, not even the
// archive that did match.
func TestALlamaArchiveThatDoesNotMatchItsPinInstallsNothing(t *testing.T) {
	archives, sums := windowsLlama(t)
	sums[winCudart] = strings.Repeat("0", 64)
	srv, _ := llamaRelease(t, "b1", archives)
	dir := t.TempDir()
	src := llamaSource{BaseURL: srv.URL, Pin: llamaPin{Tag: "b1", SHA256: sums}, GOOS: "windows", GOARCH: "amd64", Client: srv.Client()}
	if err := installLlamaWith(t.Context(), io.Discard, dir, src); !errors.Is(err, dl.ErrMismatch) {
		t.Fatalf("err %v, want download.ErrMismatch", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s holds %v after a refused archive, want nothing", dir, names)
	}
}

// An archive with no pin is never downloaded.
func TestALlamaArchiveWithoutAPinIsNeverDownloaded(t *testing.T) {
	archives, sums := windowsLlama(t)
	delete(sums, winCudart)
	srv, asked := llamaRelease(t, "b1", archives)
	src := llamaSource{BaseURL: srv.URL, Pin: llamaPin{Tag: "b1", SHA256: sums}, GOOS: "windows", GOARCH: "amd64", Client: srv.Client()}
	if err := installLlamaWith(t.Context(), io.Discard, t.TempDir(), src); err == nil {
		t.Error("no error for an archive without a pin")
	}
	for _, p := range asked() {
		if strings.HasSuffix(p, winCudart) {
			t.Errorf("downloaded %s, which has no pin", p)
		}
	}
}

// The pin built into the client covers each archive of each platform the
// client installs llama.cpp on, with a full SHA-256: a missing one fails
// here, not on a provider's machine.
func TestTheLlamaPinCoversEveryArchive(t *testing.T) {
	t.Parallel()
	p, err := loadLlamaPin()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^b\d+$`).MatchString(p.Tag) {
		t.Errorf("tag %q", p.Tag)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, plat := range [][2]string{{"windows", "amd64"}, {"darwin", "arm64"}} {
		names, err := llamaArchives(plat[0], plat[1], p.Tag)
		if err != nil || len(names) == 0 {
			t.Errorf("%s/%s: %v %v", plat[0], plat[1], names, err)
			continue
		}
		for _, name := range names {
			if !hex64.MatchString(p.SHA256[name]) {
				t.Errorf("%s/%s: %s has no full SHA-256 in llamacpp.json", plat[0], plat[1], name)
			}
		}
	}
}
