package setup

import (
	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
)

// applyFlock merges the server's flock command onto spec.
func applyFlock(cfg *config.Config, spec engine.Spec, remote winglink.Remote) (engine.Spec, error) {
	return winglink.Apply(spec, remote), nil
}
