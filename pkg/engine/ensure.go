package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// StartAndWait starts the configured engine if llm_url is down and waits until it is serving.
// A multi-node master returns once its torch store is accepting other ranks.
func StartAndWait(ctx context.Context, cfg *config.Config, logDir string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	kind, ok := ParseKind(cfg.Provider.Engine)
	if !ok {
		return fmt.Errorf("unknown engine %q", cfg.Provider.Engine)
	}
	url := cfg.Provider.LLMURL
	if Healthy(ctx, kind, url) {
		return nil
	}
	if PortOpen(ctx, url) {
		if err := WaitHealthy(ctx, kind, url, 2*time.Second, nil); err != nil {
			return fmt.Errorf("engine already bound at %s but not ready yet; check %s: %w", url, LogPath(logDir), err)
		}
		return nil
	}
	// Rank 0 binds the torch store and blocks until the other nodes connect.
	// /v1/models does not come up before that, so an open store is the ready signal.
	join := MasterJoinAddr(cfg.Provider.EngineArgs)
	if join != "" && PortOpen(ctx, "http://"+join) {
		return nil
	}

	spec, err := SpecFromConfig(cfg)
	if err != nil {
		return err
	}
	if !usableEngineBin(spec.Bin) {
		d := Detect(kind)
		if d.Unusable != "" {
			return fmt.Errorf("%s cannot run (%s); re-run flockadoo setup", Label(kind), d.Unusable)
		}
		return fmt.Errorf("%s is not installed and is not running at %s", Label(kind), url)
	}
	if kind == KindVLLM || kind == KindVLLMMetal {
		if err := CheckVersion(kind, InstalledVersion(ctx, spec.Bin)); err != nil {
			return err
		}
	}
	exited, err := StartDetached(spec, LogPath(logDir))
	if err != nil {
		return err
	}

	probeURL := url
	if probeURL == "" {
		probeURL = spec.URL
	}
	var errWait error
	if join != "" {
		errWait = WaitListen(ctx, join, 2*time.Second, exited)
	} else {
		errWait = WaitHealthy(ctx, kind, probeURL, 2*time.Second, exited)
	}
	if errWait != nil {
		return fmt.Errorf("engine started but not ready yet; check %s: %w", LogPath(logDir), errWait)
	}
	return nil
}

// SpecFromConfig rebuilds the last serve command from a saved config. A saved
// command (provider.engine_args) is used as saved, pinned to the configured
// commit.
func SpecFromConfig(cfg *config.Config) (Spec, error) {
	if cfg == nil {
		return Spec{}, fmt.Errorf("config is nil")
	}
	kind, ok := ParseKind(cfg.Provider.Engine)
	if !ok {
		return Spec{}, fmt.Errorf("unknown engine %q", cfg.Provider.Engine)
	}
	bin := BinPath(kind, firstNonEmpty(ResolveBin(kind, cfg.Provider.EngineBin), cfg.Provider.EngineBin))
	hfRepo := firstNonEmpty(cfg.Provider.HFRepo, cfg.Provider.Model)
	opts := ServeOptsFromConfig(cfg)
	if opts.Revision != "" {
		// llama.cpp serves the commit's file from the hub cache (C3).
		hub, err := verify.DefaultHFHubCache()
		if err != nil {
			return Spec{}, fmt.Errorf("finding the Hugging Face cache: %w", err)
		}
		opts.HubCache = hub
	}
	if kind != KindLlamaCpp {
		if err := opts.Validate(); err != nil {
			return Spec{}, fmt.Errorf("%w. Run flockadoo setup again to save the model's current settings", err)
		}
	}
	spec := ServeSpec(kind, bin, cfg.Provider.Model, hfRepo, opts)
	if len(cfg.Provider.EngineArgs) > 0 {
		// The saved command, on the commit the config serves now (R1).
		spec.Args, spec.Env = pinRevision(kind, slices.Clone(cfg.Provider.EngineArgs), slices.Clone(cfg.Provider.EngineEnv),
			opts.Revision, opts.HubCache, hfRepo, opts.GGUFFile)
		if cfg.Provider.LLMURL != "" {
			spec.URL = cfg.Provider.LLMURL
		}
	}
	if bin != "" {
		spec.Bin = bin
	}
	return spec, nil
}
