package setup

import (
	"context"
	"fmt"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

var (
	// wizardDownload fetches repo at sha with the client's own hf, its
	// progress going to the wizard's output.
	wizardDownload = func(ctx context.Context, repo, sha string, include []string, io Streams) error {
		return hfDownload(io.Out)(ctx, repo, sha, include)
	}
	freeBytes = hfget.FreeBytes
)

// planRevision pins the commit the wizard starts the engine on (model
// revisions C6): the C4 choice from the catalog entry the operator picked.
// It returns the download still to do, or nil when the commit is cached or
// the catalog names none. The download is not run here: it runs once the
// operator agrees to start, with the disk check first.
//
// A commit configured for another model or engine means nothing for this one,
// so it is forgotten rather than kept and reported as retired.
func planRevision(ctx context.Context, cfg *config.Config, prevModel, prevEngine string, entry verify.CatalogEntry, hub string, io Streams) (func(context.Context) error, error) {
	if prevModel != cfg.Provider.Model || config.PlatformType(prevEngine) != config.PlatformType(cfg.Provider.Engine) {
		cfg.Provider.HFRevision = ""
	}
	var pending func(context.Context) error
	err := ensureRevision(ctx, cfg, revisionDeps{
		Catalog:  func(context.Context) (verify.CatalogEntry, bool, error) { return entry, true, nil },
		HubCache: hub,
		Download: func(_ context.Context, repo, sha string, include []string) error {
			var size int64
			for _, r := range entry.Revisions {
				if r.HFRevision == sha && r.HFRepo == repo {
					size = r.SizeBytes
				}
			}
			pending = func(ctx context.Context) error {
				if err := hfget.CheckSpace(hub, size, freeBytes); err != nil {
					return fmt.Errorf("not downloading %s at %s: %w", repo, sha, err)
				}
				_, _ = fmt.Fprintf(io.Out, "Downloading %s at %s into %s...\n", repo, sha, hub)
				if err := wizardDownload(ctx, repo, sha, include, io); err != nil {
					return fmt.Errorf("downloading %s at %s: %w", repo, sha, err)
				}
				return nil
			}
			return nil
		},
		// The wizard saves the config itself, before the engine starts.
		Save: func(*config.Config) error { return nil },
		Out:  io.Out,
	})
	if err != nil {
		return nil, err
	}
	return pending, nil
}
