package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func installLlamaCpp(ctx context.Context, w io.Writer) error {
	dir, err := llamaInstallDir()
	if err != nil {
		return err
	}
	return installLlamaWith(ctx, w, dir, llamaSource{})
}

func llamaInstallDir() (string, error) {
	if runtime.GOOS == "windows" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		base := os.Getenv("LOCALAPPDATA") //nolint:forbidigo // Windows' per-user program folder (PLAT-2, item H1 moves it to a build-tagged file)
		if base == "" {
			return "", fmt.Errorf("LOCALAPPDATA is unset")
		}
		return filepath.Join(base, "flockadoo", "llama.cpp"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "flockadoo", "llama.cpp"), nil
}

func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := writeArchiveFile(dest, filepath.Base(f.Name), f.Mode(), func() (io.ReadCloser, error) {
			return f.Open()
		}); err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
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
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(hdr.Name)
		mode := hdr.FileInfo().Mode()
		if err := writeArchiveFile(dest, name, mode, func() (io.ReadCloser, error) {
			return io.NopCloser(tr), nil
		}); err != nil {
			return err
		}
	}
}

func writeArchiveFile(dest, name string, mode os.FileMode, open func() (io.ReadCloser, error)) (err error) {
	if name == "" || name == "." || strings.Contains(name, "..") {
		return nil
	}
	rc, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	outPath := filepath.Join(dest, name)
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, modeOrExec(mode))
	if err != nil {
		return err
	}
	// A failed Close can lose written data, so it is an error too.
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, rc)
	return err
}

func modeOrExec(mode os.FileMode) os.FileMode {
	if runtime.GOOS == "windows" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		return 0o644
	}
	if mode&0o111 != 0 {
		return mode
	}
	return 0o755
}
