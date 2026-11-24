package hfget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Model revisions plan, item C2: the disk-space preflight. Before hf starts,
// the client compares the revision's size plus a margin (10%, at least 2 GB)
// with the space free on the filesystem that holds the Hugging Face cache, and
// stops before downloading when it is too little. A cache folder that does not
// exist yet is judged by its nearest existing parent.

const gb = 1_000_000_000

func TestTheMarginIsTenPercentAndAtLeastTwoGB(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		size int64
		want uint64
	}{
		{10 * gb, 12 * gb}, // 10% is 1 GB: the 2 GB floor
		{50 * gb, 55 * gb}, // 10%
		{20 * gb, 22 * gb}, // both
	} {
		if got := Needed(tc.size); got != tc.want {
			t.Errorf("Needed(%d) = %d, want %d", tc.size, got, tc.want)
		}
	}
}

func TestTooLittleSpaceStopsTheDownload(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	err := CheckSpace(cache, 50*gb, func(string) (uint64, error) { return 54 * gb, nil })
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("CheckSpace = %v, want ErrNoSpace", err)
	}
	for _, want := range []string{"55.0 GB", "54.0 GB", cache, "HF_HUB_CACHE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not say %q", err, want)
		}
	}
	if err := CheckSpace(cache, 50*gb, func(string) (uint64, error) { return 55 * gb, nil }); err != nil {
		t.Errorf("CheckSpace with exactly enough = %v, want nil", err)
	}
}

func TestAMissingCacheIsJudgedByItsNearestExistingParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var asked string
	err := CheckSpace(filepath.Join(root, "huggingface", "hub"), gb, func(p string) (uint64, error) {
		asked = p
		return 100 * gb, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked != root {
		t.Errorf("free space asked of %q, want the nearest existing parent %q", asked, root)
	}
}

// A revision whose size the catalog does not know is not checked.
func TestAnUnknownSizeIsNotChecked(t *testing.T) {
	t.Parallel()
	called := false
	if err := CheckSpace(t.TempDir(), 0, func(string) (uint64, error) { called = true; return 0, nil }); err != nil || called {
		t.Errorf("CheckSpace(size 0) = %v, free asked %v; want nil, not asked", err, called)
	}
}

// FreeBytes reads the real filesystem on linux, darwin and windows (two
// build-tagged files; the check vets all three targets).
func TestFreeBytesReadsTheFilesystem(t *testing.T) {
	t.Parallel()
	free, err := FreeBytes(t.TempDir())
	if err != nil || free == 0 {
		t.Errorf("FreeBytes = %d, %v; want some space and no error", free, err)
	}
}

// model login runs Hugging Face's own prompt with the operator's terminal
// attached. The token goes from them to hf; it is never an argument.
func TestLoginRunsHFsOwnPromptOnTheTerminal(t *testing.T) {
	t.Parallel()
	in := strings.NewReader("")
	var out, errOut bytes.Buffer
	var gotName string
	var gotArgs []string
	var gotIn io.Reader
	run := func(_ context.Context, name string, args []string, i io.Reader, _, _ io.Writer) error {
		gotName, gotArgs, gotIn = name, args, i
		return nil
	}
	if err := Login(t.Context(), "/venv/bin/hf", run, in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if gotName != "/venv/bin/hf" || !slices.Equal(gotArgs, []string{"auth", "login"}) || gotIn != in {
		t.Errorf("ran %s %v with stdin %v; want /venv/bin/hf auth login on the terminal", gotName, gotArgs, gotIn)
	}
}
