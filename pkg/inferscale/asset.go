package inferscale

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
)

// release.json pins the Inferscale release this client installs: its
// version, its source commit and the SHA-256 of every file. Inferscale's
// release workflow opens a pull request that changes it, and only a
// maintainer merges it (plan item S1, decision DC6). Nothing at run time
// changes the version or a checksum.
//
//go:embed release.json
var releaseJSON []byte

// Pin is the pinned Inferscale release.
type Pin struct {
	Version string            `json:"version"`
	Commit  string            `json:"commit"`
	SHA256  map[string]string `json:"sha256"`
}

// ErrNotPinned: this build pins no file for the asset, so it is not
// downloaded or run.
var ErrNotPinned = errors.New("this build pins no Inferscale release for this platform; update the client")

func embeddedPin() (Pin, error) {
	var p Pin
	if err := json.Unmarshal(releaseJSON, &p); err != nil {
		return Pin{}, fmt.Errorf("release.json: %w", err)
	}
	return p, nil
}

// sum is the pinned SHA-256 of the named asset.
func (p Pin) sum(name string) (string, error) {
	if p.Version == "" || p.SHA256[name] == "" {
		return "", fmt.Errorf("%s: %w", name, ErrNotPinned)
	}
	return p.SHA256[name], nil
}

// Version is the pinned Inferscale release.
func Version() string {
	p, _ := embeddedPin()
	return p.Version
}

// Asset names the release files for this OS and CPU. Windows is one zip that
// also contains wintun.dll. Linux and macOS are two binaries.
func Asset(goos, goarch string) (cli, daemon string, err error) {
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", "", fmt.Errorf("unsupported arch %s", goarch)
	}
	switch goos {
	case "linux", "darwin":
		return "inferscale-" + goos + "-" + goarch, "inferscaled-" + goos + "-" + goarch, nil
	case "windows":
		return "inferscale-windows-" + goarch + ".zip", "", nil
	default:
		return "", "", fmt.Errorf("unsupported OS %s", goos)
	}
}

func downloadURLFor(p Pin, name string) string {
	return "https://github.com/Inferoute/inferoute-tailscale/releases/download/" + p.Version + "/" + name
}

func hostAsset() (cli, daemon string, err error) {
	return Asset(runtime.GOOS, runtime.GOARCH) //nolint:forbidigo // the one read of the host platform here; Asset is a pure function of goos and goarch (PLAT-2)
}
