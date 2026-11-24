package engine

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
)

//go:embed uv.json
var uvJSON []byte

// uvPin is the pinned uv release.
type uvPin struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

func loadUVPin() (uvPin, error) {
	var p uvPin
	err := json.Unmarshal(uvJSON, &p)
	return p, err
}

// uvSource is where the pinned uv comes from.
type uvSource struct {
	BaseURL string
	Pin     uvPin
	GOOS    string
	GOARCH  string
	Client  *http.Client
}

func uvAsset(goos, goarch string) (string, error) { return "", errors.New("not yet") }
