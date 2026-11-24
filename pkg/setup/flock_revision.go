package setup

import (
	"context"
	"fmt"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/verify"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
)

// applyFlock merges the server's flock command onto spec. When the server
// names the flock's commit (model revisions F1), the client takes it over its
// own configured one, since every machine must serve the same weights, and
// builds its command on it first: --revision and offline for vLLM, the
// commit's cached file for llama.cpp. A server that names none leaves both as
// they were.
func applyFlock(cfg *config.Config, spec engine.Spec, remote winglink.Remote) (engine.Spec, error) {
	if sha := strings.ToLower(strings.TrimSpace(remote.HFRevision)); sha != "" {
		cfg.Provider.HFRevision = sha
		// Built from the config alone, not from the saved flock command.
		args, env := cfg.Provider.EngineArgs, cfg.Provider.EngineEnv
		cfg.Provider.EngineArgs, cfg.Provider.EngineEnv = nil, nil
		pinned, err := engine.SpecFromConfig(cfg)
		cfg.Provider.EngineArgs, cfg.Provider.EngineEnv = args, env
		if err != nil {
			return engine.Spec{}, err
		}
		if spec.Bin != "" {
			pinned.Bin = spec.Bin
		}
		spec = pinned
	}
	return winglink.Apply(spec, remote), nil
}

// downloadFlockRevision downloads the flock's commit, with the disk check,
// before the wizard prints the command the operator runs first. With
// --no-start it says how to download it instead.
func downloadFlockRevision(cfg *config.Config, entry verify.CatalogEntry, opts Options, io Streams) error {
	sha := cfg.Provider.HFRevision
	if sha == "" {
		return nil
	}
	if opts.NoStart {
		_, _ = fmt.Fprintf(io.Out, "Download the flock's model before you start the engine: flockadoo model download --revision %s\n", sha)
		return nil
	}
	hub, err := verify.DefaultHFHubCache()
	if err != nil {
		return fmt.Errorf("finding the Hugging Face cache: %w", err)
	}
	ctx := context.Background() //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	pending, err := planRevision(ctx, cfg, cfg.Provider.Model, cfg.Provider.Engine, entry, hub, io)
	if err != nil || pending == nil {
		return err
	}
	return pending(ctx)
}
