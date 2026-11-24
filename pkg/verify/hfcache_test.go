package verify

import (
	"path/filepath"
	"testing"
)

// Where models go (the model revisions plan in flockadoo-plans): a model the
// client downloads is in the same place as one downloaded by hf, huggingface_hub
// or vLLM. The client finds the Hugging Face hub cache with Hugging Face's own
// order: HF_HUB_CACHE, HUGGINGFACE_HUB_CACHE, $HF_HOME/hub,
// $XDG_CACHE_HOME/huggingface/hub, ~/.cache/huggingface/hub. An empty variable
// counts as unset, a relative path is made absolute and ~ is the home folder.
func TestTheHubCacheIsFoundTheWayHuggingFaceFindsIt(t *testing.T) {
	home := t.TempDir()
	c1, c2 := filepath.Join(home, "hub-cache"), filepath.Join(home, "old-hub-cache")
	hfHome, xdg := filepath.Join(home, "hf-home"), filepath.Join(home, "xdg")
	relative, err := filepath.Abs(filepath.Join("rel", "cache"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, filepath.Join(home, ".cache", "huggingface", "hub")},
		{"XDG_CACHE_HOME", map[string]string{"XDG_CACHE_HOME": xdg}, filepath.Join(xdg, "huggingface", "hub")},
		{"HF_HOME", map[string]string{"HF_HOME": hfHome}, filepath.Join(hfHome, "hub")},
		{"HF_HOME before XDG_CACHE_HOME", map[string]string{"HF_HOME": hfHome, "XDG_CACHE_HOME": xdg}, filepath.Join(hfHome, "hub")},
		{"HUGGINGFACE_HUB_CACHE before HF_HOME", map[string]string{"HUGGINGFACE_HUB_CACHE": c2, "HF_HOME": hfHome}, c2},
		{"HF_HUB_CACHE before everything", map[string]string{"HF_HUB_CACHE": c1, "HUGGINGFACE_HUB_CACHE": c2, "HF_HOME": hfHome, "XDG_CACHE_HOME": xdg}, c1},
		{"a relative HF_HUB_CACHE is made absolute", map[string]string{"HF_HUB_CACHE": filepath.Join("rel", "cache")}, relative},
		{"~ in HF_HOME is the home folder", map[string]string{"HF_HOME": "~/hf-home"}, filepath.Join(home, "hf-home", "hub")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for _, k := range []string{"HF_HUB_CACHE", "HUGGINGFACE_HUB_CACHE", "HF_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(k, tc.env[k])
			}
			got, err := DefaultHFHubCache()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("hub cache %s, want %s", got, tc.want)
			}
		})
	}
}
