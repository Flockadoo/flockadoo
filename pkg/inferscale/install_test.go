package inferscale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/download"
)

// Plan item S1, follow-up (review finding, 2026-11-18): the pinned SHA-256
// decides what is installed and run as root, so the bytes checked must be the
// bytes installed. The download is checked as it arrives, in a file the
// provider's user owns, and any process running as that user can rewrite
// that file before it is copied as root.
//
// The tests cannot use sudo, so the two root operations (runRoot, writeRoot)
// are replaced by plain file operations in a temporary directory.

type fakeRoot struct {
	// userDir is the provider's temporary directory, where the download is
	// checked. When rewrite is set, every file in it is overwritten with
	// rewrite just before the copy as root starts, as another process of the
	// provider's user could.
	userDir string
	rewrite []byte

	mu  sync.Mutex
	ops []string
}

func (f *fakeRoot) use(t *testing.T) {
	t.Helper()
	oldRun, oldWrite := runRoot, writeRoot
	t.Cleanup(func() { runRoot, writeRoot = oldRun, oldWrite })
	runRoot = func(_ context.Context, args ...string) error {
		f.record(strings.Join(args, " "))
		switch args[0] {
		case "rm": // rm -f path
			if err := os.Remove(args[len(args)-1]); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		case "mv": // mv -f from to
			return os.Rename(args[len(args)-2], args[len(args)-1])
		case "chmod", "chown":
			// Mode and owner are the real runRoot's job; the test runs as
			// the provider's user and cannot give a file to root.
			return nil
		}
		return fmt.Errorf("fakeRoot: unexpected command %q", args)
	}
	writeRoot = func(_ context.Context, in io.Reader, path string) error {
		f.record("write " + path)
		if f.rewrite != nil {
			entries, err := os.ReadDir(f.userDir)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := os.WriteFile(filepath.Join(f.userDir, e.Name()), f.rewrite, 0o600); err != nil {
					return err
				}
			}
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
}

func (f *fakeRoot) record(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
}

// installRig serves body as a release asset, points the provider's temporary
// directory at a test directory, and returns the asset URL, the pin for
// body, and where installBinary is to put it.
func installRig(t *testing.T, body []byte) (fake *fakeRoot, url, sum, dest string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	userDir := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, userDir)
	}
	h := sha256.Sum256(body)
	fake = &fakeRoot{userDir: userDir}
	fake.use(t)
	return fake, srv.URL + "/inferscaled-linux-amd64", hex.EncodeToString(h[:]), filepath.Join(t.TempDir(), "inferscaled")
}

// The finding: a file rewritten after its check is never installed.
func TestABinaryChangedAfterItsCheckIsNeverInstalled(t *testing.T) {
	pinned := []byte("the pinned inferscaled")
	fake, url, sum, dest := installRig(t, pinned)
	fake.rewrite = []byte("not the pinned inferscaled")

	err := installBinary(context.Background(), url, sum, dest)

	if got, rerr := os.ReadFile(dest); rerr == nil {
		t.Fatalf("installed %q as root (err %v): the bytes changed after the check reached the install path", got, err)
	}
	if !errors.Is(err, download.ErrMismatch) {
		t.Fatalf("install returned %v, want download.ErrMismatch", err)
	}
	if _, serr := os.Stat(dest + ".tmp"); serr == nil {
		t.Fatalf("the refused copy %s.tmp was left behind", dest)
	}
}

// The positive control: an unchanged download is installed, and nothing is
// left in the provider's temporary directory or next to the binary.
func TestAVerifiedBinaryIsInstalled(t *testing.T) {
	pinned := []byte("the pinned inferscaled")
	fake, url, sum, dest := installRig(t, pinned)

	if err := installBinary(context.Background(), url, sum, dest); err != nil {
		t.Fatalf("install: %v (root operations %q)", err, fake.ops)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(pinned) {
		t.Fatalf("installed %q, %v; want the pinned file", got, err)
	}
	if _, err := os.Stat(dest + ".tmp"); err == nil {
		t.Fatalf("%s.tmp was left behind", dest)
	}
	if left, _ := os.ReadDir(fake.userDir); len(left) != 0 {
		t.Fatalf("the download was left in the temporary directory: %v", left)
	}
}
