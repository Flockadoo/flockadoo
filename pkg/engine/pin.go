package engine

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// pinRevision points a saved serve command at the configured commit (model
// revisions R1). Setup saves the command once, with the commit approved then;
// when the client later serves another commit it saves only hf_revision, so
// every start re-pins the saved command. vLLM gets --revision and
// --tokenizer-revision on rev and HF_HUB_OFFLINE=1; llama.cpp serves rev's
// cached file. Values are replaced where they are, and every other saved flag
// and variable stays. An empty rev leaves the command as saved. The slices
// passed in are not modified.
func pinRevision(kind Kind, args, env []string, rev, hub, repo, gguf string) ([]string, []string) {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return args, env
	}
	switch kind {
	case KindVLLM, KindVLLMMetal:
		args = replaceFlag(args, rev, "--revision")
		args = replaceFlag(args, rev, "--tokenizer-revision")
		env = slices.DeleteFunc(slices.Clone(env), func(e string) bool {
			return strings.HasPrefix(e, "HF_HUB_OFFLINE=") && e != "HF_HUB_OFFLINE=1"
		})
		if !slices.Contains(env, "HF_HUB_OFFLINE=1") {
			env = append(env, "HF_HUB_OFFLINE=1")
		}
		return args, env
	case KindLlamaCpp:
		if hub == "" || gguf == "" {
			return args, env
		}
		path := filepath.Join(hub, verify.HFRepoToCacheDir(repo), "snapshots", rev, filepath.FromSlash(gguf))
		// The commit's cached file replaces a fetch by repo and file.
		args = dropFlags(args, "-hf", "-hfr", "--hf-repo", "-hff", "--hf-file")
		if hasFlag(args, "-m", "--model") {
			return replaceFlag(args, path, "-m", "--model"), env
		}
		return append([]string{"-m", path}, args...), env
	}
	return args, env
}

// replaceFlag sets every occurrence of the named flag to value, in both the
// "--flag value" and "--flag=value" forms, or appends names[0] value when the
// flag is absent. args is not modified.
func replaceFlag(args []string, value string, names ...string) []string {
	out := slices.Clone(args)
	found := false
	for i := 0; i < len(out); i++ {
		if slices.Contains(names, out[i]) && i+1 < len(out) {
			out[i+1] = value
			found = true
			i++
			continue
		}
		if name, _, ok := strings.Cut(out[i], "="); ok && slices.Contains(names, name) {
			out[i] = name + "=" + value
			found = true
		}
	}
	if !found {
		out = append(out, names[0], value)
	}
	return out
}

// dropFlags returns args without the named flags and their values, in both
// forms. args is not modified.
func dropFlags(args []string, names ...string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if slices.Contains(names, args[i]) {
			i++ // and its value
			continue
		}
		if name, _, ok := strings.Cut(args[i], "="); ok && slices.Contains(names, name) {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(names, name) {
			return true
		}
	}
	return false
}
