// Package download fetches a file and checks it against a SHA-256 the
// client was built with, before anything uses it (plan item S1). It is the
// only place the client downloads something it then runs.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ErrMismatch is a download whose SHA-256 is not the pinned one.
var ErrMismatch = errors.New("downloaded file does not match its pinned SHA-256")

// Verified downloads url into a new file in dir and returns its path only if
// the file's SHA-256 is sha256hex. Without a valid pinned hash it makes no
// request. A refused download leaves nothing behind. The caller removes the
// file once it has installed it.
func Verified(ctx context.Context, client *http.Client, url, sha256hex, dir string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(sha256hex))
	if len(want) != sha256.Size*2 || strings.Trim(want, "0123456789abcdef") != "" {
		return "", fmt.Errorf("download %s: no pinned SHA-256 to check it against", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, res.Status)
	}
	f, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), res.Body)
	closeErr := f.Close()
	got := hex.EncodeToString(h.Sum(nil))
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case got != want:
		err = fmt.Errorf("%w: %s is %s, the pin is %s", ErrMismatch, url, got, want)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Matches reports whether the file at path has the SHA-256 sha256hex, so an
// installed binary is run only if it is the pinned one.
func Matches(path, sha256hex string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == strings.ToLower(strings.TrimSpace(sha256hex))
}
