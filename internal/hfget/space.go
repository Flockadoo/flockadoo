package hfget

import (
	"context"
	"errors"
	"io"
)

// ErrNoSpace is returned when the disk holding the Hugging Face cache has too
// little free space for a download.
var ErrNoSpace = errors.New("not enough free disk space")

// FreeFunc reports the bytes free to this user on the filesystem holding path.
type FreeFunc func(path string) (uint64, error)

// Needed is size plus its margin.
func Needed(size int64) uint64 { return 0 }

// CheckSpace compares size plus its margin with the space free for cache.
func CheckSpace(cache string, size int64, free FreeFunc) error { return nil }

// InteractiveRunner runs a program attached to the operator's terminal.
type InteractiveRunner func(ctx context.Context, name string, args []string, in io.Reader, out, errOut io.Writer) error

// Login runs hf's own login prompt.
func Login(ctx context.Context, hf string, run InteractiveRunner, in io.Reader, out, errOut io.Writer) error {
	return nil
}
