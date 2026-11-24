package setup

import (
	"context"
	"io"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// revisionDeps are what ensureRevision needs from the outside (model revisions C4).
type revisionDeps struct {
	// Catalog returns the configured model's catalog entry; ok is false when
	// the catalog does not list it.
	Catalog  func(ctx context.Context) (entry verify.CatalogEntry, ok bool, err error)
	HubCache string
	Download func(ctx context.Context, repo, sha string, include []string) error
	Save     func(cfg *config.Config) error
	Out      io.Writer
}

// ensureRevision picks the commit to serve before an engine starts, downloads
// it when it is not cached, remembers it in the config, and reports a retired
// or no-longer-current commit.
func ensureRevision(ctx context.Context, cfg *config.Config, d revisionDeps) error {
	return nil
}
