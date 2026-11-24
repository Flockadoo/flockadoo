package setup

import (
	"context"
	"io"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// modelDeps are what `flockadoo model` needs from the outside (model revisions C2).
type modelDeps struct {
	Config   *config.Config
	Entry    func(ctx context.Context, alias string) (verify.CatalogEntry, bool, error)
	HubCache string
	Free     hfget.FreeFunc
	Download func(ctx context.Context, repo, sha string, include []string) error
	Login    func(ctx context.Context) error
	Save     func(cfg *config.Config) error
	AsRoot   bool
	Out      io.Writer
}

func runModel(ctx context.Context, args []string, d modelDeps) error { return nil }
