package setup

import (
	"context"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

var (
	// wizardDownload fetches repo at sha with the client's own hf.
	wizardDownload = func(ctx context.Context, repo, sha string, include []string, io Streams) error {
		return hfDownload(io.Out)(ctx, repo, sha, include)
	}
	freeBytes = hfget.FreeBytes
)

// planRevision pins the commit the wizard starts the engine on.
func planRevision(ctx context.Context, cfg *config.Config, prevModel, prevEngine string, entry verify.CatalogEntry, hub string, io Streams) (func(context.Context) error, error) {
	return nil, nil
}
