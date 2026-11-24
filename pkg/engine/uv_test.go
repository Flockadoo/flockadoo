package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	dl "github.com/Flockadoo/flockadoo/internal/download"
)

// Model revisions plan, item U1. uv comes from a pinned release archive whose
// SHA-256 is built into the client (uv.json), fetched with download.Verified,
// instead of `curl https://astral.sh/uv/install.sh | bash`, which ran whatever
// that URL served and only on machines with bash. A uv already on the PATH or
// in ~/.local/bin is used as it is. Otherwise the pinned uv is unpacked into
// ~/.local/bin, where the script put it too.

const uvBody = "#!/bin/sh\necho uv 0.13.9\n"

// uvTarGz is a release archive as astral publishes it for Linux and macOS:
// uv and uvx in a folder named after the target.
func uvTarGz(t *testing.T, target string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"uvx", "uvx"}, {"uv", uvBody}} {
		if err := tw.WriteHeader(&tar.Header{Name: target + "/" + f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// uvZip is the Windows release archive: uv.exe, uvx.exe and uvw.exe at the top.
func uvZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"uvw.exe", "uv.exe", "uvx.exe"} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, uvBody); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// uvRelease serves archive at /0.13.9/<name> and counts requests.
func uvRelease(t *testing.T, name string, archive []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/0.13.9/"+name {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// noScripts fails the test if anything runs an install script.
func noScripts(t *testing.T) {
	t.Helper()
	old := runScript
	t.Cleanup(func() { runScript = old })
	runScript = func(_ context.Context, _ io.Writer, url string) error {
		t.Errorf("ran the install script %s", url)
		return errors.New("test: no install scripts")
	}
}

func TestUVIsInstalledFromThePinnedArchiveNeverAScript(t *testing.T) {
	noScripts(t)
	home := t.TempDir()
	name := "uv-x86_64-unknown-linux-gnu.tar.gz"
	archive := uvTarGz(t, "uv-x86_64-unknown-linux-gnu")
	srv, _ := uvRelease(t, name, archive)
	src := uvSource{BaseURL: srv.URL, Pin: uvPin{Version: "0.13.9", SHA256: map[string]string{name: sha256Hex(archive)}}, GOOS: "linux", GOARCH: "amd64", Client: srv.Client()}

	uv, err := ensureUVWith(t.Context(), io.Discard, home, "", src)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "bin", "uv")
	if uv != want {
		t.Errorf("uv at %s, want %s", uv, want)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != uvBody {
		t.Errorf("installed uv %q, %v; want the archive's uv", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(want)); len(entries) != 1 {
		t.Errorf("%s holds %d files, want only uv", filepath.Dir(want), len(entries))
	}
}

// A wrong checksum installs nothing and leaves nothing behind.
func TestAUVArchiveThatDoesNotMatchItsPinIsRefused(t *testing.T) {
	noScripts(t)
	home := t.TempDir()
	name := "uv-aarch64-apple-darwin.tar.gz"
	archive := uvTarGz(t, "uv-aarch64-apple-darwin")
	srv, _ := uvRelease(t, name, archive)
	src := uvSource{BaseURL: srv.URL, Pin: uvPin{Version: "0.13.9", SHA256: map[string]string{name: strings.Repeat("0", 64)}}, GOOS: "darwin", GOARCH: "arm64", Client: srv.Client()}

	if _, err := ensureUVWith(t.Context(), io.Discard, home, "", src); !errors.Is(err, dl.ErrMismatch) {
		t.Fatalf("err %v, want dl.ErrMismatch", err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if entries, err := os.ReadDir(bin); err == nil && len(entries) != 0 {
		t.Errorf("%s holds %d files after a refused download", bin, len(entries))
	}
}

// A uv already on the PATH, or in ~/.local/bin, is used as it is.
func TestAnExistingUVIsUsedAsItIs(t *testing.T) {
	noScripts(t)
	name := "uv-x86_64-unknown-linux-gnu.tar.gz"
	srv, hits := uvRelease(t, name, nil)
	src := uvSource{BaseURL: srv.URL, Pin: uvPin{Version: "0.13.9", SHA256: map[string]string{name: strings.Repeat("a", 64)}}, GOOS: "linux", GOARCH: "amd64", Client: srv.Client()}

	home := t.TempDir()
	if uv, err := ensureUVWith(t.Context(), io.Discard, home, "/usr/bin/uv", src); err != nil || uv != "/usr/bin/uv" {
		t.Errorf("uv %q, %v; want the one on the PATH", uv, err)
	}
	local := filepath.Join(home, ".local", "bin", "uv")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if uv, err := ensureUVWith(t.Context(), io.Discard, home, "", src); err != nil || uv != local {
		t.Errorf("uv %q, %v; want %s", uv, err, local)
	}
	if got, _ := os.ReadFile(local); string(got) != "mine" {
		t.Errorf("the existing uv was replaced")
	}
	if hits.Load() != 0 {
		t.Errorf("%d downloads although uv was there", hits.Load())
	}
}

// On Windows the archive is a zip and the program is uv.exe.
func TestWindowsGetsUVExe(t *testing.T) {
	noScripts(t)
	home := t.TempDir()
	name := "uv-x86_64-pc-windows-msvc.zip"
	archive := uvZip(t)
	srv, _ := uvRelease(t, name, archive)
	src := uvSource{BaseURL: srv.URL, Pin: uvPin{Version: "0.13.9", SHA256: map[string]string{name: sha256Hex(archive)}}, GOOS: "windows", GOARCH: "amd64", Client: srv.Client()}

	uv, err := ensureUVWith(t.Context(), io.Discard, home, "", src)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "bin", "uv.exe"); uv != want {
		t.Errorf("uv at %s, want %s", uv, want)
	}
}

// A platform the pin does not cover is refused before any download.
func TestAPlatformWithoutAPinnedUVIsRefused(t *testing.T) {
	noScripts(t)
	srv, hits := uvRelease(t, "x", nil)
	src := uvSource{BaseURL: srv.URL, Pin: embeddedUVPin(t), GOOS: "darwin", GOARCH: "amd64", Client: srv.Client()}
	if _, err := ensureUVWith(t.Context(), io.Discard, t.TempDir(), "", src); err == nil {
		t.Error("darwin/amd64: no error")
	}
	if hits.Load() != 0 {
		t.Errorf("%d downloads for an unpinned platform", hits.Load())
	}
}

func embeddedUVPin(t *testing.T) uvPin {
	t.Helper()
	p, err := loadUVPin()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The pin built into the client covers every platform Flockadoo installs uv
// on, each with a full SHA-256: a missing one fails here, not on a provider's
// machine.
func TestTheUVPinCoversEveryPlatform(t *testing.T) {
	t.Parallel()
	p := embeddedUVPin(t)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(p.Version) {
		t.Errorf("version %q", p.Version)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, plat := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		name, err := uvAsset(plat[0], plat[1])
		if err != nil {
			t.Errorf("%s/%s: %v", plat[0], plat[1], err)
			continue
		}
		if !hex64.MatchString(p.SHA256[name]) {
			t.Errorf("%s/%s: %s has no full SHA-256 in uv.json", plat[0], plat[1], name)
		}
	}
}
