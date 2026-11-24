//go:build !windows

package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// The macOS llama.cpp archive links its libraries by symlink:
// libllama.0.dylib -> libllama.0.7.0.dylib, libggml.dylib -> libggml.0.dylib.
// llama-server loads @rpath/libllama.0.dylib. The unpacker wrote each symlink
// as an empty file, so the installed llama-server could not load its
// libraries (found in L1's real run of b11770, 2026-11-24). A link is now
// unpacked as a link, and only to a file beside it: one pointing elsewhere is
// refused. The Windows archives are zips of plain files; symlinks there need
// privileges, so this test runs on Unix only.

type tarEntry struct {
	name, body, link string
}

func tarGzFile(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			hdr = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeSymlink, Linkname: e.link}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "llama.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestALibrarySymlinkInTheArchiveIsUnpackedAsALink(t *testing.T) {
	archive := tarGzFile(t,
		tarEntry{name: "llama-b1/libllama.0.7.0.dylib", body: "the library"},
		tarEntry{name: "llama-b1/libllama.0.dylib", link: "libllama.0.7.0.dylib"},
		tarEntry{name: "llama-b1/libllama.dylib", link: "libllama.0.dylib"},
	)
	dir := t.TempDir()
	if err := extractTarGz(archive, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"libllama.0.dylib", "libllama.dylib"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != "the library" {
			t.Errorf("%s reads %q, %v; want the library it links to", name, got, err)
		}
	}
}

// A link that points outside the install folder is not created.
func TestASymlinkOutOfTheInstallFolderIsRefused(t *testing.T) {
	for _, target := range []string{"../../etc/passwd", "/etc/passwd", "sub/../../x"} {
		archive := tarGzFile(t, tarEntry{name: "llama-b1/evil.dylib", link: target})
		dir := t.TempDir()
		_ = extractTarGz(archive, dir)
		if _, err := os.Lstat(filepath.Join(dir, "evil.dylib")); err == nil {
			if dest, _ := os.Readlink(filepath.Join(dir, "evil.dylib")); dest != "" {
				t.Errorf("link to %q was created as %q", target, dest)
			}
		}
	}
}
