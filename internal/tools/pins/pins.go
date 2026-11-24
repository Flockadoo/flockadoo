package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// options is one pins update: where the repository is, where GitHub is, and
// which releases to move to.
type options struct {
	Root     string // the repository root
	API      string // GitHub's API, https://api.github.com
	Download string // GitHub's download site, https://github.com
	Client   *http.Client
	// UVVersion is the uv release to pin; empty, the latest.
	UVVersion string
	// LlamaTag is the llama.cpp tag to pin; empty, the one pinned now. llama.cpp
	// releases several times a day, so moving it is a deliberate choice.
	LlamaTag string
	// Compile re-runs uv pip compile for the hf requirements in root.
	Compile func(ctx context.Context, root string) error
	Out     io.Writer
}

const (
	uvPinFile    = "pkg/engine/uv.json"
	llamaPinFile = "pkg/engine/llamacpp.json"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type uvPin struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

type llamaPin struct {
	Tag    string            `json:"tag"`
	SHA256 map[string]string `json:"sha256"`
}

// update fetches and checks every new pin first, compiles the hf
// requirements, and writes the pin files only then: anything missing or
// disagreeing stops it with no file changed.
func update(ctx context.Context, o options) error {
	var oldUV uvPin
	if err := readJSON(filepath.Join(o.Root, uvPinFile), &oldUV); err != nil {
		return err
	}
	var oldLlama llamaPin
	if err := readJSON(filepath.Join(o.Root, llamaPinFile), &oldLlama); err != nil {
		return err
	}

	newUV, err := o.uv(ctx, oldUV)
	if err != nil {
		return err
	}
	newLlama, err := o.llama(ctx, oldLlama)
	if err != nil {
		return err
	}
	if err := o.Compile(ctx, o.Root); err != nil {
		return fmt.Errorf("compiling the hf requirements: %w", err)
	}
	if err := writeJSON(filepath.Join(o.Root, uvPinFile), newUV); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(o.Root, llamaPinFile), newLlama); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Out, "uv %s -> %s\nllama.cpp %s -> %s\nhf requirements recompiled\nReview the diff and open a pull request; the engine-pins workflow downloads every pinned archive and checks it.\n",
		oldUV.Version, newUV.Version, oldLlama.Tag, newLlama.Tag)
	return nil
}

// uv pins the same archives as today at the new release. Each SHA-256 is
// taken only when GitHub's digest and astral's .sha256 file agree.
func (o options) uv(ctx context.Context, old uvPin) (uvPin, error) {
	path := "/repos/astral-sh/uv/releases/latest"
	if o.UVVersion != "" {
		path = "/repos/astral-sh/uv/releases/tags/" + o.UVVersion
	}
	rel, err := o.release(ctx, path)
	if err != nil {
		return uvPin{}, fmt.Errorf("uv release: %w", err)
	}
	next := uvPin{Version: rel.Tag, SHA256: map[string]string{}}
	for _, name := range sortedKeys(old.SHA256) {
		digest, err := rel.digest(name)
		if err != nil {
			return uvPin{}, fmt.Errorf("uv %s: %w", rel.Tag, err)
		}
		published, err := o.get(ctx, o.Download+"/astral-sh/uv/releases/download/"+rel.Tag+"/"+name+".sha256")
		if err != nil {
			return uvPin{}, fmt.Errorf("uv %s: %s.sha256: %w", rel.Tag, name, err)
		}
		fields := strings.Fields(string(published))
		if len(fields) == 0 || strings.ToLower(fields[0]) != digest {
			return uvPin{}, fmt.Errorf("uv %s: %s: GitHub's digest %s and astral's .sha256 %q disagree", rel.Tag, name, digest, strings.TrimSpace(string(published)))
		}
		next.SHA256[name] = digest
	}
	return next, nil
}

// llama pins the same archives at the tag (renamed when the tag moves), from
// GitHub's digests: llama.cpp publishes no checksum files of its own. The
// engine-pins workflow downloads each archive and checks it.
func (o options) llama(ctx context.Context, old llamaPin) (llamaPin, error) {
	tag := old.Tag
	if o.LlamaTag != "" {
		tag = o.LlamaTag
	}
	rel, err := o.release(ctx, "/repos/ggml-org/llama.cpp/releases/tags/"+tag)
	if err != nil {
		return llamaPin{}, fmt.Errorf("llama.cpp release %s: %w", tag, err)
	}
	next := llamaPin{Tag: rel.Tag, SHA256: map[string]string{}}
	for _, name := range sortedKeys(old.SHA256) {
		name = strings.ReplaceAll(name, old.Tag, rel.Tag)
		digest, err := rel.digest(name)
		if err != nil {
			return llamaPin{}, fmt.Errorf("llama.cpp %s: %w", rel.Tag, err)
		}
		next.SHA256[name] = digest
	}
	return next, nil
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (r release) digest(name string) (string, error) {
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		sum := strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:"))
		if !sha256Hex.MatchString(sum) {
			return "", fmt.Errorf("%s has no SHA-256 digest (%q)", name, a.Digest)
		}
		return sum, nil
	}
	return "", fmt.Errorf("the release has no %s", name)
}

func (o options) release(ctx context.Context, path string) (release, error) {
	body, err := o.get(ctx, o.API+path)
	if err != nil {
		return release{}, err
	}
	var r release
	if err := json.Unmarshal(body, &r); err != nil {
		return release{}, err
	}
	if r.Tag == "" {
		return release{}, fmt.Errorf("%s: no tag", path)
	}
	return r, nil
}

func (o options) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
