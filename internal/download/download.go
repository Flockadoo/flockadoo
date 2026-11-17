// Package download fetches a file and checks it against a SHA-256 the
// client was built with, before anything uses it (plan item S1).
package download

import (
	"context"
	"io"
	"net/http"
	"os"
)

// Verified is not written yet: plan item S1, tests first. The stub
// downloads without checking anything.
func Verified(ctx context.Context, client *http.Client, url, sha256hex, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	f, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, res.Body)
	_ = f.Close()
	return f.Name(), err
}

// Matches is not written yet: plan item S1, tests first.
func Matches(path, sha256hex string) bool { return true }
