package engine

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Model revisions plan, item C3. An engine starts on the commit the catalog
// pins, which the client downloaded (C1), and never downloads a model itself.
// vLLM gets the commit for the weights and the tokenizer and runs with
// HF_HUB_OFFLINE=1, so a missing snapshot is an error at start, never a
// silent download of main. llama.cpp serves the cached file of that commit
// with -m, not --hf-repo. R1 (2026-11-24) started this vLLM command offline
// on vLLM 0.34 (CUDA) and vLLM Metal 0.33.

const pinned = "aa8e72537993ba99e69dfaafa59ed015b17504d1"

func TestVLLMStartsOnThePinnedCommitOffline(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{KindVLLM, KindVLLMMetal} {
		spec := ServeSpec(kind, "vllm", "Qwen/Qwen2.5-3B-Instruct", "Qwen/Qwen2.5-3B-Instruct", ServeOpts{
			MaxModelLen: 16384, Revision: pinned,
		})
		join := strings.Join(spec.Args, " ")
		for _, want := range []string{"--revision " + pinned, "--tokenizer-revision " + pinned} {
			if !strings.Contains(join, want) {
				t.Errorf("%s: args %q lack %q", kind, join, want)
			}
		}
		if !slices.Contains(spec.Env, "HF_HUB_OFFLINE=1") {
			t.Errorf("%s: env %v lacks HF_HUB_OFFLINE=1", kind, spec.Env)
		}
	}
}

func TestLlamaCppServesTheCachedFileOfThePinnedCommit(t *testing.T) {
	t.Parallel()
	cache := filepath.Join("/", "cache", "hub")
	spec := ServeSpec(KindLlamaCpp, "llama-server", "qwen/qwen3.8-27b", "unsloth/Qwen3.8-27B-GGUF", ServeOpts{
		MaxModelLen: 32768, Revision: pinned, HubCache: cache,
		GGUFFile: "BF16/Qwen3.8-27B-BF16-00001-of-00002.gguf",
	})
	want := filepath.Join(cache, "models--unsloth--Qwen3.8-27B-GGUF", "snapshots", pinned, "BF16", "Qwen3.8-27B-BF16-00001-of-00002.gguf")
	i := slices.Index(spec.Args, "-m")
	if i < 0 || i+1 >= len(spec.Args) || spec.Args[i+1] != want {
		t.Errorf("args %v, want -m %s", spec.Args, want)
	}
	for _, gone := range []string{"--hf-repo", "--hf-file"} {
		if slices.Contains(spec.Args, gone) {
			t.Errorf("args %v still have %s: llama-server would download it itself", spec.Args, gone)
		}
	}
}

// Without a revision (a client against a server before model revisions, or
// an install from before them) the engine starts as before. This holds
// before the change.
func TestWithoutARevisionTheStartIsAsBefore(t *testing.T) {
	t.Parallel()
	vllm := ServeSpec(KindVLLM, "vllm", "m", "org/m", ServeOpts{MaxModelLen: 4096})
	if slices.Contains(vllm.Args, "--revision") || slices.Contains(vllm.Env, "HF_HUB_OFFLINE=1") {
		t.Errorf("vLLM without a revision: args %v env %v", vllm.Args, vllm.Env)
	}
	llama := ServeSpec(KindLlamaCpp, "llama-server", "m", "org/m-GGUF", ServeOpts{GGUFFile: "m.gguf"})
	if !slices.Contains(llama.Args, "--hf-repo") || slices.Contains(llama.Args, "-m") {
		t.Errorf("llama.cpp without a revision: args %v", llama.Args)
	}
}
