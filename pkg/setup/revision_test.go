package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C4 (second half). Before the client starts an
// engine, it picks the commit to serve from the catalog (engine.ChooseRevision),
// downloads it when it is not cached, remembers it in the config, and says
// plainly when the commit it serves is retired or no longer current. A retired
// commit is never replaced without the operator. Without the catalog it serves
// what is configured.

const (
	curSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	accSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

type revisionWorld struct {
	hub       string
	entry     verify.CatalogEntry
	catalogOK bool
	catalogEr error
	downloads [][]string // repo, sha, includes...
	dlErr     error
	saved     int
	out       bytes.Buffer
}

func newRevisionWorld(t *testing.T, revs ...verify.CatalogRevision) *revisionWorld {
	return &revisionWorld{hub: t.TempDir(), catalogOK: true, entry: verify.CatalogEntry{Alias: "org/m", Revisions: revs}}
}

func (w *revisionWorld) deps() revisionDeps {
	return revisionDeps{
		Catalog: func(context.Context) (verify.CatalogEntry, bool, error) {
			return w.entry, w.catalogOK, w.catalogEr
		},
		HubCache: w.hub,
		Download: func(_ context.Context, repo, sha string, include []string) error {
			w.downloads = append(w.downloads, append([]string{repo, sha}, include...))
			return w.dlErr
		},
		Save: func(*config.Config) error { w.saved++; return nil },
		Out:  &w.out,
	}
}

// cache puts a commit's snapshot (and file, for a GGUF) in the hub cache.
func (w *revisionWorld) cache(t *testing.T, repo, sha, file string) {
	t.Helper()
	dir := filepath.Join(w.hub, verify.HFRepoToCacheDir(repo), "snapshots", sha)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if file != "" {
		p := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func vllmConfig(revision string) *config.Config {
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "org/m"
	cfg.Provider.HFRevision = revision
	return cfg
}

var liveVLLM = []verify.CatalogRevision{
	{ServiceType: "vllm", HFRepo: "org/m", HFRevision: curSHA, Status: "current"},
	{ServiceType: "vllm", HFRepo: "org/m", HFRevision: accSHA, Status: "accepted"},
}

func TestAFreshInstallDownloadsTheCurrentCommitAndRemembersIt(t *testing.T) {
	w := newRevisionWorld(t, liveVLLM...)
	cfg := vllmConfig("")
	if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 1 || !slices.Equal(w.downloads[0], []string{"org/m", curSHA}) {
		t.Errorf("downloads %v, want org/m at the current commit", w.downloads)
	}
	if cfg.Provider.HFRevision != curSHA || w.saved != 1 {
		t.Errorf("hf_revision %q saved %d times, want %s saved once", cfg.Provider.HFRevision, w.saved, curSHA)
	}
}

func TestAnInstallKeepsTheLiveCommitItHas(t *testing.T) {
	w := newRevisionWorld(t, liveVLLM...)
	w.cache(t, "org/m", accSHA, "")
	cfg := vllmConfig("")
	if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 0 {
		t.Errorf("downloaded %v although the accepted commit is cached", w.downloads)
	}
	if cfg.Provider.HFRevision != accSHA {
		t.Errorf("hf_revision %q, want the cached accepted commit %s", cfg.Provider.HFRevision, accSHA)
	}
	if !strings.Contains(w.out.String(), "flockadoo model download") {
		t.Errorf("no notice that a newer commit is current: %q", w.out.String())
	}
}

// A retired commit is kept and reported: the server no longer routes to it,
// and the current one is a download the operator starts.
func TestARetiredCommitIsReportedNeverReplaced(t *testing.T) {
	w := newRevisionWorld(t, liveVLLM...)
	w.cache(t, "org/m", oldSHA, "")
	cfg := vllmConfig(oldSHA)
	if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 0 || w.saved != 0 || cfg.Provider.HFRevision != oldSHA {
		t.Errorf("a retired commit was replaced: downloads %v, saved %d, hf_revision %s", w.downloads, w.saved, cfg.Provider.HFRevision)
	}
	for _, want := range []string{"retired", oldSHA, curSHA, "flockadoo model download"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("notice %q does not say %q", w.out.String(), want)
		}
	}
}

func TestAConfiguredLiveCommitThatIsCachedChangesNothing(t *testing.T) {
	w := newRevisionWorld(t, liveVLLM...)
	w.cache(t, "org/m", curSHA, "")
	cfg := vllmConfig(curSHA)
	if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
		t.Fatal(err)
	}
	if len(w.downloads) != 0 || w.saved != 0 || w.out.Len() != 0 {
		t.Errorf("downloads %v, saved %d, output %q; want nothing", w.downloads, w.saved, w.out.String())
	}
}

// A GGUF downloads only its file, or its folder of shards.
func TestAGGUFDownloadIsLimitedToItsFile(t *testing.T) {
	for _, tc := range []struct{ file, include string }{
		{"gpt-oss-20b-MXFP4.gguf", "gpt-oss-20b-MXFP4.gguf"},
		{"BF16/Qwen3.8-27B-BF16-00001-of-00002.gguf", "BF16/*"},
	} {
		file := tc.file
		w := newRevisionWorld(t, verify.CatalogRevision{ServiceType: "llama_cpp", HFRepo: "org/m-GGUF", HFRevision: curSHA, GGUFFile: &file, Status: "current"})
		cfg := &config.Config{}
		cfg.Provider.Engine = "llama_cpp"
		cfg.Provider.Model = "org/m"
		if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
			t.Fatal(err)
		}
		if len(w.downloads) != 1 || !slices.Equal(w.downloads[0], []string{"org/m-GGUF", curSHA, tc.include}) {
			t.Errorf("%s: downloads %v, want org/m-GGUF at %s including %s", tc.file, w.downloads, curSHA, tc.include)
		}
		if cfg.Provider.HFRepo != "org/m-GGUF" || cfg.Provider.GGUFFile != tc.file {
			t.Errorf("%s: config repo %q file %q, want the revision's", tc.file, cfg.Provider.HFRepo, cfg.Provider.GGUFFile)
		}
	}
}

// Without the catalog the client serves what is configured; a failed
// download stops the start, since the engine has no model to serve.
func TestWithoutTheCatalogTheConfiguredCommitIsServed(t *testing.T) {
	w := newRevisionWorld(t)
	w.catalogEr = errors.New("connection refused")
	cfg := vllmConfig(curSHA)
	if err := ensureRevision(t.Context(), cfg, w.deps()); err != nil {
		t.Fatalf("an unreadable catalog stopped the start: %v", err)
	}
	if len(w.downloads) != 0 || cfg.Provider.HFRevision != curSHA {
		t.Errorf("downloads %v, hf_revision %s", w.downloads, cfg.Provider.HFRevision)
	}

	w = newRevisionWorld(t, liveVLLM...)
	w.dlErr = errors.New("disk full")
	if err := ensureRevision(t.Context(), vllmConfig(""), w.deps()); err == nil {
		t.Error("a failed download did not stop the start")
	}
}
