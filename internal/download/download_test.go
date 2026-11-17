package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Plan item S1 (findings CLU-1, SEC-6): a downloaded file is used only if
// its SHA-256 equals the one the client was built with.

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func serve(t *testing.T, body []byte) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestAFileWithThePinnedHashIsHandedOver(t *testing.T) {
	body := []byte("the real inferscale binary")
	srv, _ := serve(t, body)
	path, err := Verified(context.Background(), srv.Client(), srv.URL+"/inferscale", sum(body), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Fatalf("handed over %q", got)
	}
}

// A wrong hash is refused, and nothing is left behind to install or run.
func TestAFileWithAnotherHashIsRefusedAndRemoved(t *testing.T) {
	srv, _ := serve(t, []byte("a tampered binary"))
	dir := t.TempDir()
	if _, err := Verified(context.Background(), srv.Client(), srv.URL+"/inferscale", sum([]byte("the real one")), dir); err == nil {
		t.Fatal("a file whose hash differs from the pin was accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a refused download left %v behind", left)
	}
}

// No pinned hash means no download at all: there is no path that skips the
// check.
func TestNoPinnedHashMeansNoDownload(t *testing.T) {
	srv, hits := serve(t, []byte("anything"))
	for _, pin := range []string{"", "not-a-sha256"} {
		if _, err := Verified(context.Background(), srv.Client(), srv.URL+"/inferscale", pin, t.TempDir()); err == nil {
			t.Fatalf("pin %q: a download with no valid pinned hash was accepted", pin)
		}
	}
	if *hits != 0 {
		t.Fatalf("%d requests were made without a pinned hash", *hits)
	}
}

// An installed file is checked against the pin before it is run.
func TestAnInstalledFileIsCheckedAgainstThePin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inferscaled")
	if err := os.WriteFile(path, []byte("an old binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Matches(path, sum([]byte("the pinned binary"))) {
		t.Fatal("an installed file that differs from the pin was accepted")
	}
	if !Matches(path, sum([]byte("an old binary"))) {
		t.Fatal("an installed file that matches the pin was refused")
	}
	if Matches(filepath.Join(t.TempDir(), "missing"), sum(nil)) {
		t.Fatal("a missing file matched")
	}
}
