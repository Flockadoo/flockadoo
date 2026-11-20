package engine

import (
	"errors"
	"fmt"

	"github.com/Flockadoo/flockadoo/pkg/nodeapi"
)

// MinVLLMVersion is the oldest vLLM release Flockadoo supports, for vLLM and
// vLLM Metal (which reports the vLLM release it runs). The YaRN override is a
// complete rope_parameters dict (command.go), which needs the transformers 5
// handling every release from 0.30 has; an older vLLM reads rope_scaling,
// ignores the override and serves past the model's context with no error.
// flockadoo-server refuses a cluster pin below the same release
// (pkg/flock.MinVLLMVersion).
const MinVLLMVersion = "0.30.0"

// ErrEngineTooOld marks an engine release older than Flockadoo supports.
var ErrEngineTooOld = errors.New("unsupported engine release")

// TooOld reports whether version is a vLLM or vLLM Metal release older than
// MinVLLMVersion. llama.cpp builds are never too old, and a version that cannot
// be read ("", "dev") is not treated as old.
func TooOld(kind Kind, version string) bool {
	if kind != KindVLLM && kind != KindVLLMMetal {
		return false
	}
	return nodeapi.Newer(MinVLLMVersion, version)
}

// CheckVersion is TooOld as an error that names the release, the minimum and
// what to do; nil when the release is supported or unknown.
func CheckVersion(kind Kind, version string) error {
	if !TooOld(kind, version) {
		return nil
	}
	return fmt.Errorf("%w: %s %s is older than Flockadoo supports (vLLM %s or newer). Run flockadoo setup to install a supported release",
		ErrEngineTooOld, Label(kind), version, MinVLLMVersion)
}
