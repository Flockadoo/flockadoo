package setup

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C6. After the model is chosen, the wizard pins
// the commit to serve (the C4 choice), downloads it with progress and the
// disk check (C1, C2), and only then starts the engine on that commit (C3),
// so the first start never waits silently on a download of main. A failed
// download stops before the start. Choosing another model, or another engine,
// forgets the commit of the old one.

// stoppedEngine is an engine URL nothing listens on.
func stoppedEngine(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()
	return url
}

// recordStarts replaces the engine start, recording it in order, and stops
// maybeStart there.
func recordStarts(t *testing.T, order *[]string) {
	t.Helper()
	old := startDetached
	t.Cleanup(func() { startDetached = old })
	startDetached = func(engine.Spec, string) (<-chan error, error) {
		*order = append(*order, "start")
		return nil, errors.New("test: stopped at the start")
	}
}

func startConfig(t *testing.T) *config.Config {
	cfg := vllmConfig("")
	cfg.Provider.LLMURL = stoppedEngine(t)
	cfg.Logging.LogDir = t.TempDir()
	return cfg
}

var vllmSpec = engine.Spec{Bin: "/bin/vllm", Args: []string{"serve", "org/m"}}

func TestTheWizardDownloadsBeforeItStartsTheEngine(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	download := func(context.Context) error { order = append(order, "download"); return nil }
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, download)
	if !slices.Equal(order, []string{"download", "start"}) {
		t.Errorf("order %v, want the download, then the start", order)
	}
}

func TestAFailedDownloadStopsBeforeTheStart(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	download := func(context.Context) error { return fmt.Errorf("%w: needs 55.0 GB", hfget.ErrNoSpace) }
	var out strings.Builder
	err := maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, download)
	if !errors.Is(err, hfget.ErrNoSpace) {
		t.Errorf("err %v, want the download's error", err)
	}
	if len(order) != 0 {
		t.Errorf("the engine started after a failed download: %v", order)
	}
}

// Declining the start leaves the download to the operator, and says how.
func TestDecliningTheStartSaysHowToDownload(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	downloaded := false
	download := func(context.Context) error { downloaded = true; return nil }
	var out strings.Builder
	if err := maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{}, true, Streams{In: strings.NewReader("n\n"), Out: &out, Err: &out}, download); err != nil {
		t.Fatal(err)
	}
	if downloaded || len(order) != 0 {
		t.Errorf("declined, yet downloaded %v and ran %v", downloaded, order)
	}
	if !strings.Contains(out.String(), "flockadoo model download") {
		t.Errorf("output %q does not say how to download", out.String())
	}
}

// revisionEntry is org/m with a current and an accepted vLLM revision.
func revisionEntry() verify.CatalogEntry {
	return verify.CatalogEntry{Alias: "org/m", Revisions: []verify.CatalogRevision{
		{ServiceType: "vllm", HFRepo: "org/m", HFRevision: curSHA, Status: "current", SizeBytes: 50 * sizedGB},
		{ServiceType: "vllm", HFRepo: "org/m", HFRevision: accSHA, Status: "accepted", SizeBytes: 50 * sizedGB},
	}}
}

// fakeFetch replaces the real hf download and free-space reading.
func fakeFetch(t *testing.T, free uint64, got *[][]string) {
	t.Helper()
	oldDL, oldFree := wizardDownload, freeBytes
	t.Cleanup(func() { wizardDownload, freeBytes = oldDL, oldFree })
	wizardDownload = func(_ context.Context, repo, sha string, include []string, _ Streams) error {
		*got = append(*got, append([]string{repo, sha}, include...))
		return nil
	}
	freeBytes = func(string) (uint64, error) { return free, nil }
}

func TestTheWizardPinsTheCurrentCommitAndDownloadsItWithTheSpaceCheck(t *testing.T) {
	w := newRevisionWorld(t)
	var got [][]string
	fakeFetch(t, 1000*sizedGB, &got)
	cfg := vllmConfig("")
	pending, err := planRevision(t.Context(), cfg, "org/m", "vllm", revisionEntry(), w.hub, Streams{Out: &w.out})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.HFRevision != curSHA {
		t.Errorf("hf_revision %q, want the current commit %s", cfg.Provider.HFRevision, curSHA)
	}
	if pending == nil {
		t.Fatal("nothing to download, though nothing is cached")
	}
	if len(got) != 0 {
		t.Errorf("downloaded %v before the operator agreed to start", got)
	}
	if err := pending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !slices.Equal(got[0], []string{"org/m", curSHA}) {
		t.Errorf("downloads %v, want org/m at the current commit", got)
	}

	// Too little space: hf never runs.
	got = nil
	fakeFetch(t, 54*sizedGB, &got)
	cfg = vllmConfig("")
	pending, err = planRevision(t.Context(), cfg, "org/m", "vllm", revisionEntry(), w.hub, Streams{Out: &w.out})
	if err != nil || pending == nil {
		t.Fatalf("planRevision = %v, pending %v", err, pending != nil)
	}
	if err := pending(t.Context()); !errors.Is(err, hfget.ErrNoSpace) || len(got) != 0 {
		t.Errorf("with 54 GB free: err %v, downloads %v; want ErrNoSpace and none", err, got)
	}
}

func TestAReRunKeepsTheCachedCommitItServes(t *testing.T) {
	w := newRevisionWorld(t)
	w.cache(t, "org/m", accSHA, "")
	var got [][]string
	fakeFetch(t, 1000*sizedGB, &got)
	cfg := vllmConfig(accSHA)
	pending, err := planRevision(t.Context(), cfg, "org/m", "vllm", revisionEntry(), w.hub, Streams{Out: &w.out})
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil || cfg.Provider.HFRevision != accSHA {
		t.Errorf("pending %v, hf_revision %q; want nothing to download and %s kept", pending != nil, cfg.Provider.HFRevision, accSHA)
	}
}

// Another model, or another engine, makes the old commit meaningless: it is
// forgotten, not reported as retired and kept.
func TestChoosingAnotherModelOrEngineForgetsTheOldCommit(t *testing.T) {
	for _, tc := range []struct{ name, prevModel, prevEngine string }{
		{"another model", "org/old", "vllm"},
		{"another engine", "org/m", "llama_cpp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRevisionWorld(t)
			var got [][]string
			fakeFetch(t, 1000*sizedGB, &got)
			cfg := vllmConfig(oldSHA)
			if _, err := planRevision(t.Context(), cfg, tc.prevModel, tc.prevEngine, revisionEntry(), w.hub, Streams{Out: &w.out}); err != nil {
				t.Fatal(err)
			}
			if cfg.Provider.HFRevision != curSHA {
				t.Errorf("hf_revision %q, want the new model's current commit %s", cfg.Provider.HFRevision, curSHA)
			}
			if strings.Contains(w.out.String(), "retired") {
				t.Errorf("the old model's commit was reported as retired: %q", w.out.String())
			}
		})
	}
}

// A catalog without revisions (a server before them) leaves setup as it was.
func TestACatalogWithoutRevisionsLeavesSetupAsItWas(t *testing.T) {
	w := newRevisionWorld(t)
	var got [][]string
	fakeFetch(t, 1000*sizedGB, &got)
	cfg := vllmConfig("")
	pending, err := planRevision(t.Context(), cfg, "org/m", "vllm", verify.CatalogEntry{Alias: "org/m"}, w.hub, Streams{Out: &w.out})
	if err != nil || pending != nil || cfg.Provider.HFRevision != "" {
		t.Errorf("err %v, pending %v, hf_revision %q; want as before", err, pending != nil, cfg.Provider.HFRevision)
	}
}
