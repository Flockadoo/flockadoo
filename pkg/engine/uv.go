package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"

	dl "github.com/Flockadoo/flockadoo/internal/download"
)

// uv.json pins the uv release the client installs when no uv is present: its
// version and the SHA-256 of each release archive, as astral publishes them
// (model revisions plan, item U1). Moving the pin is a pull request, like the
// Winglink pin; nothing at run time changes it.
//
//go:embed uv.json
var uvJSON []byte

// uvReleases is where astral publishes uv's release archives.
const uvReleases = "https://github.com/astral-sh/uv/releases/download"

// uvPin is the pinned uv release.
type uvPin struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

func loadUVPin() (uvPin, error) {
	var p uvPin
	if err := json.Unmarshal(uvJSON, &p); err != nil {
		return uvPin{}, fmt.Errorf("uv.json: %w", err)
	}
	return p, nil
}

// uvSource is where the pinned uv comes from: the release site, the pin, the
// platform and the HTTP client. The zero value is the real one.
type uvSource struct {
	BaseURL string
	Pin     uvPin
	GOOS    string
	GOARCH  string
	Client  *http.Client
}

func (s uvSource) withDefaults() (uvSource, error) {
	if s.BaseURL == "" {
		s.BaseURL = uvReleases
	}
	if s.Pin.Version == "" {
		p, err := loadUVPin()
		if err != nil {
			return s, err
		}
		s.Pin = p
	}
	if s.GOOS == "" {
		s.GOOS, s.GOARCH = runtime.GOOS, runtime.GOARCH //nolint:forbidigo // the one read of the host platform here; uvAsset is a pure function of it (PLAT-2)
	}
	if s.Client == nil {
		s.Client = downloadClient
	}
	return s, nil
}

// uvAsset names uv's release archive for a platform Flockadoo installs uv on.
func uvAsset(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "uv-x86_64-unknown-linux-gnu.tar.gz", nil
	case "linux/arm64":
		return "uv-aarch64-unknown-linux-gnu.tar.gz", nil
	case "darwin/arm64":
		return "uv-aarch64-apple-darwin.tar.gz", nil
	case "windows/amd64":
		return "uv-x86_64-pc-windows-msvc.zip", nil
	}
	return "", fmt.Errorf("this client pins no uv for %s/%s", goos, goarch)
}

// installPinnedUV downloads the pinned uv archive for the platform, checks it
// against its SHA-256 before anything reads it, and puts uv (uv.exe on
// Windows) into ~/.local/bin. A refused download leaves nothing behind.
func installPinnedUV(ctx context.Context, w io.Writer, home string, src uvSource) (string, error) {
	src, err := src.withDefaults()
	if err != nil {
		return "", err
	}
	name, err := uvAsset(src.GOOS, src.GOARCH)
	if err != nil {
		return "", err
	}
	sum := src.Pin.SHA256[name]
	exe := "uv"
	if src.GOOS == "windows" {
		exe = "uv.exe"
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", err
	}
	url := src.BaseURL + "/" + src.Pin.Version + "/" + name
	_, _ = fmt.Fprintf(w, "Installing uv %s from %s...\n", src.Pin.Version, url)
	archive, err := dl.Verified(ctx, src.Client, url, sum, bin)
	if err != nil {
		return "", fmt.Errorf("install uv: %w", err)
	}
	defer func() { _ = os.Remove(archive) }()

	tmp, err := os.CreateTemp(bin, ".uv-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if path.Ext(name) == ".zip" {
		err = copyFromZip(archive, exe, tmp)
	} else {
		err = copyFromTarGz(archive, exe, tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("install uv: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(bin, exe)
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}
	return dest, nil
}

var errNoUVInArchive = errors.New("the uv archive holds no uv program")

// copyFromTarGz copies the file named exe (at any depth) out of a .tar.gz.
func copyFromTarGz(archive, exe string, out io.Writer) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errNoUVInArchive
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == exe {
			_, err := io.Copy(out, tr) //nolint:gosec // G110: the archive's SHA-256 was checked against the pin before it was opened
			return err
		}
	}
}

// copyFromZip copies the file named exe (at any depth) out of a .zip.
func copyFromZip(archive, exe string, out io.Writer) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != exe {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(out, rc) //nolint:gosec // G110: the archive's SHA-256 was checked against the pin before it was opened
		_ = rc.Close()
		return err
	}
	return errNoUVInArchive
}
