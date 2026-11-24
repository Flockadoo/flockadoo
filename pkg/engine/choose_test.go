package engine

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C4. Which commit the client serves: the
// configured one while it is live (downloaded first if it is not cached); with
// none configured, a cached live one, the current one first, so an install
// from before revisions keeps what it has; otherwise the current one,
// downloaded. A retired one is kept and reported, never replaced without the
// operator: a download can be 20 to 60 GB. A catalog without revisions (a
// server before them) leaves the start as it was.

const (
	current  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	accepted = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	retired  = "cccccccccccccccccccccccccccccccccccccccc"
)

var revisions = []verify.CatalogRevision{
	{ServiceType: "vllm", HFRepo: "org/m", HFRevision: current, Status: "current"},
	{ServiceType: "vllm", HFRepo: "org/m", HFRevision: accepted, Status: "accepted"},
	// A merged card also carries the other engine's revisions; they are not ours.
	{ServiceType: "llama_cpp", HFRepo: "org/m-GGUF", HFRevision: "dddddddddddddddddddddddddddddddddddddddd", Status: "current"},
}

func cachedSet(shas ...string) func(string) bool {
	return func(sha string) bool { return slices.Contains(shas, sha) }
}

func TestTheRevisionTheClientServes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		revs       []verify.CatalogRevision
		configured string
		cached     []string
		want       Choice
	}{
		{"configured and live, cached", revisions, current, []string{current}, Choice{Revision: current, Status: "current", Current: current}},
		{"configured and live, not cached: download it", revisions, accepted, nil, Choice{Revision: accepted, Status: "accepted", Current: current, Download: true}},
		{"configured and retired: keep it, never download", revisions, retired, nil, Choice{Revision: retired, Status: "retired", Current: current}},
		{"none configured, current cached", revisions, "", []string{current, accepted}, Choice{Revision: current, Status: "current", Current: current}},
		{"none configured, only the accepted one cached", revisions, "", []string{accepted}, Choice{Revision: accepted, Status: "accepted", Current: current}},
		{"none configured, nothing cached: download the current one", revisions, "", nil, Choice{Revision: current, Status: "current", Current: current, Download: true}},
		{"the other engine's revision is not ours", revisions, "dddddddddddddddddddddddddddddddddddddddd", nil, Choice{Revision: "dddddddddddddddddddddddddddddddddddddddd", Status: "retired", Current: current}},
		{"a catalog without revisions: as before", nil, "", nil, Choice{}},
		{"a catalog without revisions keeps a configured one", nil, current, nil, Choice{Revision: current}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChooseRevision(tc.revs, "vllm", tc.configured, cachedSet(tc.cached...))
			if got != tc.want {
				t.Errorf("%+v, want %+v", got, tc.want)
			}
		})
	}
}

// A configured revision reaches the start command: vLLM starts on it offline,
// llama.cpp serves its file from the hub cache.
func TestTheConfiguredRevisionReachesTheStart(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)

	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "Qwen/Qwen2.5-3B-Instruct"
	cfg.Provider.HFRevision = current
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(spec.Args, " "), "--revision "+current) {
		t.Errorf("vLLM args %v lack --revision %s", spec.Args, current)
	}

	cfg = &config.Config{}
	cfg.Provider.Engine = "llama_cpp"
	cfg.Provider.Model = "openai/gpt-oss-20b"
	cfg.Provider.HFRepo = "ggml-org/gpt-oss-20b-GGUF"
	cfg.Provider.GGUFFile = "gpt-oss-20b-MXFP4.gguf"
	cfg.Provider.HFRevision = current
	spec, err = SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "models--ggml-org--gpt-oss-20b-GGUF", "snapshots", current, "gpt-oss-20b-MXFP4.gguf")
	if i := slices.Index(spec.Args, "-m"); i < 0 || spec.Args[i+1] != want {
		t.Errorf("llama.cpp args %v, want -m %s", spec.Args, want)
	}
}
