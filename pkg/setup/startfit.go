package setup

import (
	"context"
	"fmt"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// checkStartFit refuses an engine start whose model does not fit the memory
// free now (engine start memory plan, E3). Tests replace it.
var checkStartFit = realStartFit

// startFitDeps are the catalog and hardware the check reads.
type startFitDeps struct {
	catalog func(ctx context.Context, cfg *config.Config) (verify.CatalogEntry, bool, error)
	detect  func(ctx context.Context) (*compat.Hardware, error)
}

func realStartFit(ctx context.Context, cfg *config.Config, orphans []engine.Orphan) error {
	return startFitWith(ctx, cfg, orphans, startFitDeps{
		catalog: func(ctx context.Context, cfg *config.Config) (verify.CatalogEntry, bool, error) {
			c := verify.NewCatalog(cfg.Provider.URL, cfg.PlatformType())
			if err := c.Refresh(ctx); err != nil {
				return verify.CatalogEntry{}, false, err
			}
			e, ok := c.Get(cfg.Provider.Model)
			return e, ok, nil
		},
		detect: compat.Detect,
	})
}

// startFitWith is the check: nil when the model fits, or when it cannot be
// told (no catalog entry, no free-memory reading, a flock).
func startFitWith(ctx context.Context, cfg *config.Config, orphans []engine.Orphan, d startFitDeps) error {
	// A flock's memory is planned across its machines.
	if role := cfg.Provider.FlockRole; role == "master" || role == "worker" {
		return nil
	}
	entry, hw, ok := fitInputs(ctx, cfg, d)
	if !ok {
		return nil
	}
	err := compat.StartCheck(hw, entry, cfg.Provider.MaxModelLen)
	if err == nil {
		return nil
	}
	if hint := orphanHint(orphans); hint != "" {
		return fmt.Errorf("%w. %s", err, hint)
	}
	return err
}

// fitInputs reads the model's catalog entry and the machine's memory. It is
// not ok when either cannot be read: the client cannot tell, and starts.
func fitInputs(ctx context.Context, cfg *config.Config, d startFitDeps) (verify.CatalogEntry, *compat.Hardware, bool) {
	entry, listed, err := d.catalog(ctx, cfg)
	if err != nil || !listed {
		return verify.CatalogEntry{}, nil, false
	}
	hw, err := d.detect(ctx)
	if err != nil || hw == nil {
		return verify.CatalogEntry{}, nil, false
	}
	return entry, hw, true
}
