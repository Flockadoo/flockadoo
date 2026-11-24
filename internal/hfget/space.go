package hfget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// ErrNoSpace is returned when the disk holding the Hugging Face cache has too
// little free space for a download.
var ErrNoSpace = errors.New("not enough free disk space")

// FreeFunc reports the bytes free to this user on the filesystem holding path.
type FreeFunc func(path string) (uint64, error)

// minMargin is the least room kept beyond a download's size.
const minMargin = 2_000_000_000

// Needed is size plus a margin of 10%, at least 2 GB (model revisions C2).
// It does not subtract blobs already in the cache, so it errs on the safe side.
func Needed(size int64) uint64 {
	margin := uint64(size) / 10
	if margin < minMargin {
		margin = minMargin
	}
	return uint64(size) + margin
}

// CheckSpace compares size plus its margin with the space free on the
// filesystem that holds cache, or its nearest existing parent while cache does
// not exist yet. A size of zero or less is unknown, and not checked.
func CheckSpace(cache string, size int64, free FreeFunc) error {
	if size <= 0 {
		return nil
	}
	dir := existingParent(cache)
	have, err := free(dir)
	if err != nil {
		return fmt.Errorf("reading the free space on %s: %w", dir, err)
	}
	if need := Needed(size); have < need {
		return fmt.Errorf("%w: the download needs %s with its margin, and %s is free on the disk holding %s; point HF_HUB_CACHE at a folder on a bigger disk",
			ErrNoSpace, gigabytes(need), gigabytes(have), dir)
	}
	return nil
}

// existingParent is path, or its nearest parent that exists.
func existingParent(path string) string {
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		up := filepath.Dir(p)
		if up == p {
			return p
		}
		p = up
	}
}

func gigabytes(n uint64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// InteractiveRunner runs a program attached to the operator's terminal.
type InteractiveRunner func(ctx context.Context, name string, args []string, in io.Reader, out, errOut io.Writer) error

// ExecInteractive runs a program with exec.CommandContext and the given
// terminal streams. It sets no environment, so the program inherits the client's.
func ExecInteractive(ctx context.Context, name string, args []string, in io.Reader, out, errOut io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is the hf in the client's own venv, and args are built here, not taken from input
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}

// Login runs hf's own login prompt with the operator's terminal attached
// (decision D5). The token goes from the operator to hf, which keeps it in
// Hugging Face's standard place; it is never an argument, and Flockadoo never
// reads or stores it.
func Login(ctx context.Context, hf string, run InteractiveRunner, in io.Reader, out, errOut io.Writer) error {
	if err := run(ctx, hf, []string{"auth", "login"}, in, out, errOut); err != nil {
		return fmt.Errorf("hf auth login: %w", err)
	}
	return nil
}
