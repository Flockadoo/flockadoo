package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"

	dl "github.com/Flockadoo/flockadoo/internal/download"
)

// llamacpp.json pins the ggml-org/llama.cpp release the client installs on
// Windows and macOS: its tag and the SHA-256 of each archive, from GitHub's
// published asset digests (model revisions plan, item L1). The tag moves
// only when the serve flags or the cache file names change; llama.cpp
// releases several times a day. Moving it is a pull request; nothing at run
// time changes it.
//
//go:embed llamacpp.json
var llamaJSON []byte

// llamaReleases is where ggml-org publishes llama.cpp's release archives.
const llamaReleases = "https://github.com/ggml-org/llama.cpp/releases/download"

// llamaPin is the pinned llama.cpp release.
type llamaPin struct {
	Tag    string            `json:"tag"`
	SHA256 map[string]string `json:"sha256"`
}

func loadLlamaPin() (llamaPin, error) {
	var p llamaPin
	if err := json.Unmarshal(llamaJSON, &p); err != nil {
		return llamaPin{}, fmt.Errorf("llamacpp.json: %w", err)
	}
	return p, nil
}

// llamaSource is where the pinned llama.cpp comes from: the release site, the
// pin, the platform and the HTTP client. The zero value is the real one.
type llamaSource struct {
	BaseURL string
	Pin     llamaPin
	GOOS    string
	GOARCH  string
	Client  *http.Client
}

func (s llamaSource) withDefaults() (llamaSource, error) {
	if s.BaseURL == "" {
		s.BaseURL = llamaReleases
	}
	if s.Pin.Tag == "" {
		p, err := loadLlamaPin()
		if err != nil {
			return s, err
		}
		s.Pin = p
	}
	if s.GOOS == "" {
		s.GOOS, s.GOARCH = runtime.GOOS, runtime.GOARCH //nolint:forbidigo // the one read of the host platform here; llamaArchives is a pure function of it (PLAT-2)
	}
	if s.Client == nil {
		s.Client = downloadClient
	}
	return s, nil
}

// llamaArchives names the release archives installed on a platform, in order.
func llamaArchives(goos, goarch, tag string) ([]string, error) {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return []string{"llama-" + tag + "-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-12.4-x64.zip"}, nil
	case "darwin/arm64":
		return []string{"llama-" + tag + "-bin-macos-arm64.tar.gz"}, nil
	case "darwin/amd64":
		return nil, fmt.Errorf("llama.cpp auto-install on macOS requires Apple Silicon")
	}
	return nil, fmt.Errorf("llama.cpp auto-install is only supported on Windows and macOS")
}

// installLlamaWith downloads every archive for the platform, checks each
// against its pinned SHA-256, and only then unpacks them into dir. An archive
// without a pin is never downloaded; one that does not match installs nothing.
func installLlamaWith(ctx context.Context, w io.Writer, dir string, src llamaSource) error {
	src, err := src.withDefaults()
	if err != nil {
		return err
	}
	names, err := llamaArchives(src.GOOS, src.GOARCH, src.Pin.Tag)
	if err != nil {
		return err
	}
	for _, name := range names {
		if src.Pin.SHA256[name] == "" {
			return fmt.Errorf("this client pins no checksum for %s; update the client", name)
		}
	}
	tmp, err := os.MkdirTemp("", "llama-cpp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	_, _ = fmt.Fprintf(w, "Installing llama.cpp %s into %s...\n", src.Pin.Tag, dir)
	files := make([]string, 0, len(names))
	for _, name := range names {
		url := src.BaseURL + "/" + src.Pin.Tag + "/" + name
		_, _ = fmt.Fprintf(w, "Downloading %s\n", url)
		f, err := dl.Verified(ctx, src.Client, url, src.Pin.SHA256[name], tmp)
		if err != nil {
			return fmt.Errorf("install llama.cpp: %w", err)
		}
		files = append(files, f)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, f := range files {
		extract := extractZip
		if strings.HasSuffix(names[i], ".tar.gz") {
			extract = extractTarGz
		}
		if err := extract(f, dir); err != nil {
			return fmt.Errorf("unpacking %s: %w", names[i], err)
		}
	}
	return nil
}
