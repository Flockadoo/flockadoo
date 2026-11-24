package setup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
)

// Model revisions plan, item F1 (client half). Every machine in a flock serves
// the commit the server names in the engine response (hf_revision: the
// master's live commit, else the current one). The client takes it over its
// own configured commit, starts its command on it (--revision and offline for
// vLLM, the cached file for llama.cpp), and downloads it before it shows the
// command the operator runs. A server that names none leaves the start as it
// was.

func flockRemote(sha string) winglink.Remote {
	return winglink.Remote{
		Host: "198.18.0.2", Port: 8000, Role: "worker", HFRevision: sha,
		ExtraArgs: []string{"--nnodes", "2", "--node-rank", "1", "--master-addr", "198.18.0.1", "--headless"},
		Env:       []string{"VLLM_HOST_IP=198.18.0.2"},
	}
}

func flockVLLMConfig(revision string) *config.Config {
	cfg := vllmConfig(revision)
	cfg.Provider.MaxModelLen = 4096
	return cfg
}

func TestTheFlocksCommitIsTheOneEveryMachineServes(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	cfg := flockVLLMConfig(oldSHA) // configured before; the flock's commit wins
	base := engine.ServeSpec(engine.KindVLLM, "/bin/vllm", "org/m", "org/m", engine.ServeOpts{MaxModelLen: 4096})
	got, err := applyFlock(cfg, base, flockRemote(curSHA))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.HFRevision != curSHA {
		t.Errorf("hf_revision %q, want the flock's %s", cfg.Provider.HFRevision, curSHA)
	}
	args := strings.Join(got.Args, " ")
	for _, want := range []string{"--revision " + curSHA, "--tokenizer-revision " + curSHA, "--headless", "--host 198.18.0.2"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if n := strings.Count(args, "--revision "); n != 1 {
		t.Errorf("args %q carry --revision %d times, want once", args, n)
	}
	for _, want := range []string{"HF_HUB_OFFLINE=1", "VLLM_HOST_IP=198.18.0.2"} {
		if !slices.Contains(got.Env, want) {
			t.Errorf("env %v lacks %s", got.Env, want)
		}
	}
}

func TestALlamaCppFlockServesTheCachedFileOfTheFlocksCommit(t *testing.T) {
	hub := t.TempDir()
	t.Setenv("HF_HUB_CACHE", hub)
	cfg := &config.Config{}
	cfg.Provider.Engine = "llama_cpp"
	cfg.Provider.Model = "openai/gpt-oss-20b"
	cfg.Provider.HFRepo = "ggml-org/gpt-oss-20b-GGUF"
	cfg.Provider.GGUFFile = "gpt-oss-20b-MXFP4.gguf"
	base := engine.ServeSpec(engine.KindLlamaCpp, "/bin/llama-server", cfg.Provider.Model, cfg.Provider.HFRepo, engine.ServeOpts{GGUFFile: cfg.Provider.GGUFFile})
	remote := winglink.Remote{Host: "198.18.0.2", Port: 8080, HFRevision: curSHA}
	got, err := applyFlock(cfg, base, remote)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(hub, "models--ggml-org--gpt-oss-20b-GGUF", "snapshots", curSHA, "gpt-oss-20b-MXFP4.gguf")
	if i := slices.Index(got.Args, "-m"); i < 0 || got.Args[i+1] != want {
		t.Errorf("args %v, want -m %s", got.Args, want)
	}
}

// A server that names no commit (before F1, or a build without revisions)
// leaves the command and the configured commit as they were.
func TestAFlockWithoutACommitStartsAsBefore(t *testing.T) {
	cfg := flockVLLMConfig(accSHA)
	base := engine.ServeSpec(engine.KindVLLM, "/bin/vllm", "org/m", "org/m", engine.ServeOpts{MaxModelLen: 4096})
	got, err := applyFlock(cfg, base, flockRemote(""))
	if err != nil {
		t.Fatal(err)
	}
	if want := winglink.Apply(base, flockRemote("")); !slices.Equal(got.Args, want.Args) || !slices.Equal(got.Env, want.Env) {
		t.Errorf("spec %v %v, want as before %v %v", got.Args, got.Env, want.Args, want.Env)
	}
	if cfg.Provider.HFRevision != accSHA {
		t.Errorf("hf_revision %q, want %s kept", cfg.Provider.HFRevision, accSHA)
	}
}

// At start, a flock member downloads the commit before it shows the command
// the operator runs; a failed download shows no command.
func TestAFlockMemberDownloadsBeforeItShowsTheCommand(t *testing.T) {
	for _, role := range []string{"worker", "master"} {
		t.Run(role, func(t *testing.T) {
			stubFlockRefresh(t)
			cfg := flockVLLMConfig(curSHA)
			cfg.Provider.FlockRole = role
			cfg.Provider.LLMURL = "http://127.0.0.1:1"
			cfg.Provider.EngineArgs = []string{"serve", "org/m", "--nnodes", "2", "--master-addr", "127.0.0.1", "--master-port", "1", "--revision", curSHA}
			var out bytes.Buffer
			revision := func(_ context.Context, c *config.Config) error {
				_, _ = io.WriteString(&out, "DOWNLOADED "+c.Provider.HFRevision+"\n")
				return nil
			}
			if err := ensureOnStartWith(t.Context(), cfg, Streams{In: strings.NewReader(""), Out: &out}, false, revision); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			d, c := strings.Index(text, "DOWNLOADED "+curSHA), strings.Index(text, "serve org/m")
			if d < 0 || c < 0 || d > c {
				t.Errorf("want the download before the command:\n%s", text)
			}

			out.Reset()
			failing := func(context.Context, *config.Config) error { return errors.New("no space") }
			if err := ensureOnStartWith(t.Context(), cfg, Streams{In: strings.NewReader(""), Out: &out}, false, failing); err == nil {
				t.Error("a failed download did not stop the start")
			}
			if strings.Contains(out.String(), "serve org/m") {
				t.Errorf("the command was shown after a failed download:\n%s", out.String())
			}
		})
	}
}

// The setup wizard for a flock downloads the flock's commit before it prints
// the command the operator is told to run first.
func TestTheFlockWizardDownloadsTheFlocksCommit(t *testing.T) {
	singlePlan(t)
	flockPlan = func(*config.Config) (flockSettings, error) {
		return flockSettings{Role: "master", Name: "flockadoo-flock1", Model: "Qwen/Qwen3-0.6B", ServiceType: "vllm"}, nil
	}
	oldPrepare := prepareFlock
	t.Cleanup(func() { prepareFlock = oldPrepare })
	prepareFlock = func(cfg *config.Config, _ *engine.Spec) error {
		cfg.Provider.FlockRole = "master"
		cfg.Provider.HFRevision = curSHA // what applyFlock takes from the engine response
		return nil
	}
	oldDetect := detectEngine
	t.Cleanup(func() { detectEngine = oldDetect })
	detectEngine = func(kind engine.Kind) engine.Detected {
		return engine.Detected{Kind: kind, Found: true, Bin: "/opt/vllm/bin/vllm"}
	}
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	var got [][]string
	fakeFetch(t, 1000*sizedGB, &got)

	catalog := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(catalog, []byte(`{"object":"list","data":[
		{"id":"2","alias":"Qwen/Qwen3-0.6B","service_type":"vllm","hf_repo":"Qwen/Qwen3-0.6B","min_size_bytes":1000000,"max_model_len":4096,"is_active":true,"engines":["vllm","vllm-metal"],
		 "revisions":[{"service_type":"vllm","hf_repo":"Qwen/Qwen3-0.6B","hf_revision":"`+curSHA+`","status":"current","size_bytes":1000000}]},
		{"id":"4","alias":"Qwen/Qwen3-0.6B","service_type":"llama_cpp","hf_repo":"Qwen/Qwen3-0.6B-GGUF","gguf_file":"q.gguf","min_size_bytes":1000000,"is_active":true,"engines":["llama_cpp"],
		 "revisions":[{"service_type":"llama_cpp","hf_repo":"Qwen/Qwen3-0.6B-GGUF","hf_revision":"`+curSHA+`","gguf_file":"q.gguf","status":"current","size_bytes":1000000}]}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	err := Execute(Options{
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), APIKey: "sk-flock",
		OfflineCatalog: catalog, Yes: true,
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf})
	if err != nil {
		t.Fatalf("Execute: %v\n%s\n%s", err, out.String(), errBuf.String())
	}
	if len(got) != 1 || got[0][1] != curSHA {
		t.Errorf("downloads %v, want the flock's commit %s once", got, curSHA)
	}
	if d, c := strings.Index(out.String(), "Downloading"), strings.Index(out.String(), "Start vLLM first"); d < 0 || c < 0 || d > c {
		t.Errorf("want the download before the command:\n%s", out.String())
	}
}
