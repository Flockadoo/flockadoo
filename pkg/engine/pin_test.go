package engine

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// Model revisions plan, item R1 (gap found on the re-run, 2026-11-25). Setup
// saves the serve command once, pinned to the commit approved then
// (provider.engine_args). When the approved commit changes, the client saves
// the new provider.hf_revision but the saved command still names the old one,
// so the engine served commit A while the verifier measured, and reported,
// commit B. Every start pins the saved command to the configured commit:
// vLLM gets --revision and --tokenizer-revision on it and HF_HUB_OFFLINE=1;
// llama.cpp serves that commit's cached file. Flags the operator or the flock
// added stay.

// The saved command as a 0.46 setup writes it for commit A (accepted), with a
// flag the operator added by hand.
func savedVLLM(revision string) *config.Config {
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "org/m"
	cfg.Provider.HFRevision = revision
	cfg.Provider.EngineArgs = []string{"serve", "org/m", "--host", "127.0.0.1", "--port", "8000",
		"--revision", accepted, "--tokenizer-revision", accepted, "--gpu-memory-utilization", "0.7"}
	cfg.Provider.EngineEnv = []string{"HF_HUB_OFFLINE=1", "VLLM_HOST_IP=10.0.0.2"}
	return cfg
}

func flagValues(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestASavedVLLMCommandStartsOnTheConfiguredCommit(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	spec, err := SpecFromConfig(savedVLLM(current))
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--revision", "--tokenizer-revision"} {
		if got := flagValues(spec.Args, flag); !slices.Equal(got, []string{current}) {
			t.Errorf("%s = %v, want only %s: %v", flag, got, current, spec.Args)
		}
	}
	// The operator's own flags and the saved environment stay.
	if got := flagValues(spec.Args, "--gpu-memory-utilization"); !slices.Equal(got, []string{"0.7"}) {
		t.Errorf("--gpu-memory-utilization = %v, want the saved 0.7: %v", got, spec.Args)
	}
	if !slices.Contains(spec.Env, "VLLM_HOST_IP=10.0.0.2") {
		t.Errorf("env %v lost VLLM_HOST_IP", spec.Env)
	}
}

// A command saved before 0.46 has no revision flags and no HF_HUB_OFFLINE: it
// gets both, so a missing snapshot fails the start instead of fetching main.
func TestACommandSavedBeforeRevisionsIsPinnedAndOffline(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm-metal"
	cfg.Provider.Model = "baai/bge-m3"
	cfg.Provider.HFRevision = current
	cfg.Provider.EngineArgs = []string{"serve", "baai/bge-m3", "--host", "127.0.0.1", "--port", "8000"}
	cfg.Provider.EngineEnv = []string{"HF_HUB_OFFLINE=0"}
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--revision", "--tokenizer-revision"} {
		if got := flagValues(spec.Args, flag); !slices.Equal(got, []string{current}) {
			t.Errorf("%s = %v, want %s: %v", flag, got, current, spec.Args)
		}
	}
	var offline []string
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "HF_HUB_OFFLINE=") {
			offline = append(offline, e)
		}
	}
	if !slices.Equal(offline, []string{"HF_HUB_OFFLINE=1"}) {
		t.Errorf("env %v, want HF_HUB_OFFLINE=1 once", spec.Env)
	}
}

// The --flag=value form a hand-edited command may use is replaced too.
func TestAnEqualsFormRevisionIsReplaced(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	cfg := savedVLLM(current)
	cfg.Provider.EngineArgs = []string{"serve", "org/m", "--revision=" + accepted, "--tokenizer-revision=" + accepted}
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(spec.Args, " "); strings.Contains(line, accepted) {
		t.Errorf("args %v still name the old commit", spec.Args)
	}
	if got := flagValues(spec.Args, "--revision"); !slices.Equal(got, []string{current}) {
		t.Errorf("--revision = %v, want %s", got, current)
	}
}

func TestASavedLlamaCppCommandServesTheConfiguredCommitsFile(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)
	cfg := &config.Config{}
	cfg.Provider.Engine = "llama_cpp"
	cfg.Provider.Model = "openai/gpt-oss-20b"
	cfg.Provider.HFRepo = "ggml-org/gpt-oss-20b-GGUF"
	cfg.Provider.GGUFFile = "gpt-oss-20b-MXFP4.gguf"
	cfg.Provider.HFRevision = current
	old := filepath.Join(cache, "models--ggml-org--gpt-oss-20b-GGUF", "snapshots", accepted, "gpt-oss-20b-MXFP4.gguf")
	cfg.Provider.EngineArgs = []string{"-m", old, "--alias", "openai/gpt-oss-20b", "--host", "127.0.0.1", "--port", "8081", "-ngl", "99", "--metrics", "-c", "32768"}
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "models--ggml-org--gpt-oss-20b-GGUF", "snapshots", current, "gpt-oss-20b-MXFP4.gguf")
	if got := flagValues(spec.Args, "-m"); !slices.Equal(got, []string{want}) {
		t.Errorf("-m = %v, want only %s: %v", got, want, spec.Args)
	}
	if got := flagValues(spec.Args, "-c"); !slices.Equal(got, []string{"32768"}) {
		t.Errorf("-c = %v, want the saved 32768: %v", got, spec.Args)
	}
}

// A llama.cpp command saved before revisions fetches by repo and file; with a
// configured commit it serves the cached file instead.
func TestALlamaCppCommandSavedBeforeRevisionsServesTheCachedFile(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)
	cfg := &config.Config{}
	cfg.Provider.Engine = "llama_cpp"
	cfg.Provider.Model = "openai/gpt-oss-20b"
	cfg.Provider.HFRepo = "ggml-org/gpt-oss-20b-GGUF"
	cfg.Provider.GGUFFile = "gpt-oss-20b-MXFP4.gguf"
	cfg.Provider.HFRevision = current
	cfg.Provider.EngineArgs = []string{"--hf-repo", "ggml-org/gpt-oss-20b-GGUF", "--hf-file", "gpt-oss-20b-MXFP4.gguf", "--alias", "openai/gpt-oss-20b", "--port", "8081"}
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "models--ggml-org--gpt-oss-20b-GGUF", "snapshots", current, "gpt-oss-20b-MXFP4.gguf")
	if got := flagValues(spec.Args, "-m"); !slices.Equal(got, []string{want}) {
		t.Errorf("-m = %v, want %s: %v", got, want, spec.Args)
	}
	if slices.Contains(spec.Args, "--hf-repo") || slices.Contains(spec.Args, "--hf-file") {
		t.Errorf("args %v still fetch by repo", spec.Args)
	}
	if got := flagValues(spec.Args, "--alias"); !slices.Equal(got, []string{"openai/gpt-oss-20b"}) {
		t.Errorf("--alias = %v: %v", got, spec.Args)
	}
}

// Negative: with no configured commit (a catalog without revisions, or one
// that could not be read at setup) the saved command is used exactly as saved.
func TestWithoutAConfiguredCommitTheSavedCommandIsUnchanged(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	cfg := savedVLLM("")
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Args, cfg.Provider.EngineArgs) || !slices.Equal(spec.Env, cfg.Provider.EngineEnv) {
		t.Errorf("spec %v %v, want the saved %v %v", spec.Args, spec.Env, cfg.Provider.EngineArgs, cfg.Provider.EngineEnv)
	}
}

// Pinning is the same on every start: a command already on the configured
// commit comes out unchanged, and the saved config is not modified.
func TestACommandAlreadyOnTheCommitIsUnchanged(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	cfg := savedVLLM(accepted)
	saved := slices.Clone(cfg.Provider.EngineArgs)
	spec, err := SpecFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Args, saved) {
		t.Errorf("args %v, want the saved %v", spec.Args, saved)
	}
	if !slices.Equal(cfg.Provider.EngineArgs, saved) {
		t.Errorf("SpecFromConfig changed the saved command to %v", cfg.Provider.EngineArgs)
	}
}
