package setup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C2 (first half): `flockadoo model download
// [alias] [--revision sha]` and `flockadoo model login`.
//
//   - download takes the configured alias and its current revision by default;
//     --revision must be a live revision of that alias for this engine, since
//     downloading anything else is pointless;
//   - it checks free disk space first (the revision's size plus a margin) and
//     stops before hf runs when there is too little, unless --skip-space-check;
//   - when the alias is the configured one, it remembers the commit in the
//     config, so the next start serves it;
//   - a gated repo is explained step by step, ending in `flockadoo model login`;
//   - run as root without HF_HUB_CACHE it refuses: it would download into
//     root's home, where the client never looks;
//   - login runs Hugging Face's own prompt; the token never passes through
//     Flockadoo.

const sizedGB = 1_000_000_000

type modelWorld struct {
	*revisionWorld
	entries map[string]verify.CatalogEntry
	cfg     *config.Config
	free    uint64
	logins  int
	asRoot  bool
}

func newModelWorld(t *testing.T) *modelWorld {
	w := &modelWorld{
		revisionWorld: newRevisionWorld(t),
		entries: map[string]verify.CatalogEntry{
			"org/m": {Alias: "org/m", Revisions: []verify.CatalogRevision{
				{ServiceType: "vllm", HFRepo: "org/m", HFRevision: curSHA, Status: "current", SizeBytes: 50 * sizedGB},
				{ServiceType: "vllm", HFRepo: "org/m", HFRevision: accSHA, Status: "accepted", SizeBytes: 50 * sizedGB},
			}},
			"org/other": {Alias: "org/other", Revisions: []verify.CatalogRevision{
				{ServiceType: "vllm", HFRepo: "org/other", HFRevision: oldSHA, Status: "current", SizeBytes: 5 * sizedGB},
			}},
		},
		cfg:  vllmConfig(""),
		free: 1000 * sizedGB,
	}
	return w
}

func (w *modelWorld) deps() modelDeps {
	r := w.revisionWorld.deps()
	return modelDeps{
		Config: w.cfg,
		Entry: func(_ context.Context, alias string) (verify.CatalogEntry, bool, error) {
			e, ok := w.entries[alias]
			return e, ok, nil
		},
		HubCache: w.hub,
		Free:     func(string) (uint64, error) { return w.free, nil },
		Download: r.Download,
		Login:    func(context.Context) error { w.logins++; return nil },
		Save:     r.Save,
		AsRoot:   w.asRoot,
		Out:      &w.out,
	}
}

func (w *modelWorld) run(t *testing.T, args ...string) error {
	t.Helper()
	return runModel(t.Context(), args, w.deps())
}

func TestModelDownloadFetchesTheConfiguredModelsCurrentCommitAndRemembersIt(t *testing.T) {
	w := newModelWorld(t)
	if err := w.run(t, "download"); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 1 || !slices.Equal(w.downloads[0], []string{"org/m", curSHA}) {
		t.Errorf("downloads %v, want org/m at the current commit", w.downloads)
	}
	if w.cfg.Provider.HFRevision != curSHA || w.saved != 1 {
		t.Errorf("hf_revision %q saved %d times, want %s saved once", w.cfg.Provider.HFRevision, w.saved, curSHA)
	}
}

func TestModelDownloadTakesALiveRevision(t *testing.T) {
	w := newModelWorld(t)
	if err := w.run(t, "download", "org/m", "--revision", accSHA); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 1 || !slices.Equal(w.downloads[0], []string{"org/m", accSHA}) {
		t.Errorf("downloads %v, want org/m at the accepted commit", w.downloads)
	}
	if w.cfg.Provider.HFRevision != accSHA {
		t.Errorf("hf_revision %q, want %s", w.cfg.Provider.HFRevision, accSHA)
	}
}

// Another alias is downloaded, and the configured model is left alone.
func TestModelDownloadOfAnotherAliasLeavesTheConfigAlone(t *testing.T) {
	w := newModelWorld(t)
	if err := w.run(t, "download", "org/other"); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 1 || !slices.Equal(w.downloads[0], []string{"org/other", oldSHA}) {
		t.Errorf("downloads %v, want org/other at its current commit", w.downloads)
	}
	if w.saved != 0 || w.cfg.Provider.HFRevision != "" {
		t.Errorf("config changed: saved %d, hf_revision %q", w.saved, w.cfg.Provider.HFRevision)
	}
}

// A commit already in the cache is not fetched again, and is remembered.
func TestModelDownloadOfACachedCommitIsRememberedNotFetched(t *testing.T) {
	w := newModelWorld(t)
	w.cache(t, "org/m", curSHA, "")
	if err := w.run(t, "download"); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 0 {
		t.Errorf("downloaded %v although the commit is cached", w.downloads)
	}
	if w.cfg.Provider.HFRevision != curSHA {
		t.Errorf("hf_revision %q, want %s", w.cfg.Provider.HFRevision, curSHA)
	}
}

func TestModelDownloadRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"an alias outside the catalog", []string{"download", "org/unknown"}, "org/unknown"},
		{"a commit that is not live", []string{"download", "--revision", oldSHA}, oldSHA},
		{"a commit of another alias", []string{"download", "org/m", "--revision", oldSHA}, "not a live revision"},
		{"a short commit", []string{"download", "--revision", "abc123"}, "abc123"},
		{"two aliases", []string{"download", "org/m", "org/other"}, "one model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newModelWorld(t)
			err := w.run(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want a refusal naming %q", err, tc.want)
			}
			if len(w.downloads) != 0 || w.saved != 0 {
				t.Errorf("downloads %v, saved %d after a refusal", w.downloads, w.saved)
			}
		})
	}
}

// With less free space than the size plus its margin, hf never runs.
func TestModelDownloadStopsBeforeHFWithTooLittleSpace(t *testing.T) {
	w := newModelWorld(t)
	w.free = 54 * sizedGB // 50 GB needs 55
	err := w.run(t, "download")
	if !errors.Is(err, hfget.ErrNoSpace) {
		t.Fatalf("err %v, want ErrNoSpace", err)
	}
	if len(w.downloads) != 0 {
		t.Errorf("hf ran: %v", w.downloads)
	}
	if !strings.Contains(err.Error(), "--skip-space-check") {
		t.Errorf("message %q does not offer --skip-space-check", err)
	}

	if err := w.run(t, "download", "--skip-space-check"); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 1 {
		t.Errorf("--skip-space-check: downloads %v, want one", w.downloads)
	}
}

func TestModelDownloadExplainsAGatedRepo(t *testing.T) {
	w := newModelWorld(t)
	w.dlErr = fmt.Errorf("%w: org/m", hfget.ErrGated)
	if err := w.run(t, "download"); !errors.Is(err, hfget.ErrGated) {
		t.Fatalf("err %v, want ErrGated", err)
	}
	for _, want := range []string{"https://huggingface.co/org/m", "https://huggingface.co/settings/tokens", "flockadoo model login"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("steps %q do not say %q", w.out.String(), want)
		}
	}
	if w.saved != 0 {
		t.Errorf("config saved after a refused download")
	}
}

func TestModelDownloadAsRootWithoutHFHubCacheRefuses(t *testing.T) {
	w := newModelWorld(t)
	w.asRoot = true
	err := w.run(t, "download")
	if err == nil || !strings.Contains(err.Error(), "HF_HUB_CACHE") {
		t.Errorf("err %v, want a refusal naming HF_HUB_CACHE", err)
	}
	if len(w.downloads) != 0 {
		t.Errorf("downloaded as root: %v", w.downloads)
	}
}

func TestModelLoginRunsHuggingFacesPrompt(t *testing.T) {
	w := newModelWorld(t)
	if err := w.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if w.logins != 1 {
		t.Errorf("login ran %d times, want once", w.logins)
	}
}

func TestModelWithoutASubcommandIsAnError(t *testing.T) {
	w := newModelWorld(t)
	for _, args := range [][]string{nil, {"fly"}} {
		if err := w.run(t, args...); err == nil {
			t.Errorf("model %v: no error", args)
		}
	}
}

// At engine start (C4) the same preflight runs before the download: with too
// little space the engine does not start, and hf never runs.
func TestStartStopsBeforeDownloadingWithTooLittleSpace(t *testing.T) {
	w := newRevisionWorld(t, verify.CatalogRevision{ServiceType: "vllm", HFRepo: "org/m", HFRevision: curSHA, Status: "current", SizeBytes: 50 * sizedGB})
	var asked int64
	d := w.deps()
	d.Space = func(size int64) error {
		asked = size
		return fmt.Errorf("%w: needs 55.0 GB", hfget.ErrNoSpace)
	}
	err := ensureRevision(t.Context(), vllmConfig(""), d)
	if !errors.Is(err, hfget.ErrNoSpace) {
		t.Fatalf("err %v, want ErrNoSpace", err)
	}
	if asked != 50*sizedGB || len(w.downloads) != 0 {
		t.Errorf("space asked for %d bytes, downloads %v; want 50 GB asked and no download", asked, w.downloads)
	}
}
