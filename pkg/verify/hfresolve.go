package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultHFHubCache returns the Hugging Face hub cache directory, found the
// way huggingface_hub finds it, so the client, hf and vLLM use one folder
// (the model revisions plan, "Where models go"): HF_HUB_CACHE, then
// HUGGINGFACE_HUB_CACHE, then $HF_HOME/hub, where HF_HOME defaults to
// $XDG_CACHE_HOME/huggingface and XDG_CACHE_HOME to ~/.cache.
func DefaultHFHubCache() (string, error) {
	return hubCacheDir(os.Getenv, os.UserHomeDir) //nolint:forbidigo // mirrors the HuggingFace hub's own cache lookup, which reads these variables
}

// hubCacheDir is DefaultHFHubCache with the environment and home folder given.
// An empty variable counts as unset; ~ is the home folder; the result is absolute.
func hubCacheDir(getenv func(string) string, userHome func() (string, error)) (string, error) {
	expand := func(p string) (string, error) {
		if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
			home, err := userHome()
			if err != nil {
				return "", err
			}
			p = filepath.Join(home, p[1:])
		}
		return filepath.Abs(p)
	}
	for _, key := range []string{"HF_HUB_CACHE", "HUGGINGFACE_HUB_CACHE"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return expand(v)
		}
	}
	hfHome := strings.TrimSpace(getenv("HF_HOME"))
	if hfHome == "" {
		cache := strings.TrimSpace(getenv("XDG_CACHE_HOME"))
		if cache == "" {
			cache = "~/.cache"
		}
		hfHome = filepath.Join(cache, "huggingface")
	}
	home, err := expand(hfHome)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hub"), nil
}

// HFRepoToCacheDir converts Qwen/Qwen3-0.6B to models--Qwen--Qwen3-0.6B.
func HFRepoToCacheDir(repoID string) string {
	return "models--" + strings.ReplaceAll(repoID, "/", "--")
}

func dirHasWeights(root string) bool {
	if st, err := os.Stat(filepath.Join(root, "config.json")); err == nil && !st.IsDir() {
		return true
	}
	return false
}

// ResolveHFModelRoot locates on-disk weights for a HuggingFace repo in the hub cache.
// hubCache is typically ~/.cache/huggingface/hub; revision is the pinned commit SHA.
func ResolveHFModelRoot(hubCache, repoID, revision string) (string, error) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		return "", fmt.Errorf("empty huggingface repo id")
	}

	if hubCache == "" {
		var err error
		hubCache, err = DefaultHFHubCache()
		if err != nil {
			return "", err
		}
	}
	hubCache, err := filepath.Abs(hubCache)
	if err != nil {
		return "", err
	}

	cacheDir := filepath.Join(hubCache, HFRepoToCacheDir(repoID))
	if revision != "" {
		return snapshotForRevision(cacheDir, revision)
	}

	// No pinned revision: use refs/main if present, else the only snapshot.
	if snap, err := snapshotForRevision(cacheDir, "main"); err == nil {
		return snap, nil
	}

	snapshotsDir := filepath.Join(cacheDir, "snapshots")
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return "", fmt.Errorf("no snapshots under %s: %w", cacheDir, err)
	}
	var found string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(snapshotsDir, e.Name())
		if !dirHasWeights(candidate) {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("multiple snapshots under %s; pin hf_revision in approved builds", cacheDir)
		}
		found = candidate
	}
	if found == "" {
		return "", fmt.Errorf("no weight files found under %s", cacheDir)
	}
	return found, nil
}

// snapshotForRevision resolves a HF hub revision to a snapshots/ directory.
// revision may be a commit SHA (snapshots/<sha>) or a ref name (refs/<name> → snapshots/<sha>).
func snapshotForRevision(cacheDir, revision string) (string, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "", fmt.Errorf("empty revision")
	}

	snap := filepath.Join(cacheDir, "snapshots", revision)
	if dirHasWeights(snap) {
		return snap, nil
	}

	refFile := filepath.Join(cacheDir, "refs", revision)
	data, err := os.ReadFile(refFile)
	if err != nil {
		return "", fmt.Errorf("revision %s not found under %s", revision, cacheDir)
	}
	sha := strings.TrimSpace(string(data))
	if sha == "" {
		return "", fmt.Errorf("revision %s not found under %s", revision, cacheDir)
	}
	snap = filepath.Join(cacheDir, "snapshots", sha)
	if dirHasWeights(snap) {
		return snap, nil
	}
	return "", fmt.Errorf("revision %s not found under %s", revision, cacheDir)
}
