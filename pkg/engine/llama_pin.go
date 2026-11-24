package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

//go:embed llamacpp.json
var llamaJSON []byte

type llamaPin struct {
	Tag    string            `json:"tag"`
	SHA256 map[string]string `json:"sha256"`
}

func loadLlamaPin() (llamaPin, error) {
	var p llamaPin
	err := json.Unmarshal(llamaJSON, &p)
	return p, err
}

type llamaSource struct {
	BaseURL string
	Pin     llamaPin
	GOOS    string
	GOARCH  string
	Client  *http.Client
}

func llamaArchives(goos, goarch, tag string) ([]string, error) { return nil, errors.New("not yet") }

// installLlamaWith: today's unchecked download, at src's address.
func installLlamaWith(ctx context.Context, w io.Writer, dir string, src llamaSource) error {
	base := src.BaseURL + "/" + src.Pin.Tag
	if src.GOOS == "windows" {
		if err := downloadAndExtract(ctx, w, base+"/llama-"+src.Pin.Tag+"-bin-win-cuda-12.4-x64.zip", dir); err != nil {
			return err
		}
		return downloadAndExtract(ctx, w, base+"/cudart-llama-bin-win-cuda-12.4-x64.zip", dir)
	}
	return downloadAndExtract(ctx, w, base+"/llama-"+src.Pin.Tag+"-bin-macos-arm64.tar.gz", dir)
}
