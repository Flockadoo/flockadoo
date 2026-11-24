package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/engine"
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

var ggufShard = regexp.MustCompile(`-\d{5}-of-\d{5}\.gguf$`)

// ensureRevision picks the commit to serve before an engine starts, downloads
// it when it is not cached, remembers it in the config, and reports a retired
// or no-longer-current commit. A retired commit is never replaced here: the
// download can be 20 to 60 GB, and the operator starts it.
func ensureRevision(ctx context.Context, cfg *config.Config, d revisionDeps) error {
	entry, ok, err := d.Catalog(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(d.Out, "Could not read the model catalog (%v); serving %s as configured.\n", err, cfg.Provider.Model)
		return nil
	}
	if !ok {
		return nil
	}
	service := config.PlatformType(cfg.Provider.Engine)
	byRev := map[string]verify.CatalogRevision{}
	for _, r := range entry.Revisions {
		if r.ServiceType == service {
			byRev[r.HFRevision] = r
		}
	}
	cached := func(sha string) bool {
		repo, file := firstNonEmpty(cfg.Provider.HFRepo, cfg.Provider.Model), cfg.Provider.GGUFFile
		if r, ok := byRev[sha]; ok {
			repo, file = r.HFRepo, ggufOf(r)
		}
		p := filepath.Join(d.HubCache, verify.HFRepoToCacheDir(repo), "snapshots", sha)
		if file != "" {
			p = filepath.Join(p, filepath.FromSlash(file))
		}
		_, err := os.Stat(p)
		return err == nil
	}
	choice := engine.ChooseRevision(entry.Revisions, service, cfg.Provider.HFRevision, cached)
	if choice.Revision == "" {
		return nil
	}
	rev, known := byRev[choice.Revision]
	if choice.Download && known {
		if err := d.Download(ctx, rev.HFRepo, choice.Revision, ggufInclude(rev)); err != nil {
			return fmt.Errorf("downloading %s at %s: %w", rev.HFRepo, choice.Revision, err)
		}
	}
	if known && choice.Revision != cfg.Provider.HFRevision {
		cfg.Provider.HFRevision = choice.Revision
		cfg.Provider.HFRepo = rev.HFRepo
		if f := ggufOf(rev); f != "" {
			cfg.Provider.GGUFFile = f
		}
		if err := d.Save(cfg); err != nil {
			return fmt.Errorf("remembering the commit to serve: %w", err)
		}
	}
	switch choice.Status {
	case "retired":
		_, _ = fmt.Fprintf(d.Out, "The commit you serve for %s, %s, is retired: the server no longer routes to it. The current commit is %s; download it with `flockadoo model download`, then restart.\n",
			cfg.Provider.Model, choice.Revision, choice.Current)
	case "accepted":
		_, _ = fmt.Fprintf(d.Out, "A newer commit of %s is current (%s). The one you serve, %s, is still accepted until the next approval retires it; download the current one with `flockadoo model download`.\n",
			cfg.Provider.Model, choice.Current, choice.Revision)
	}
	return nil
}

func ggufOf(r verify.CatalogRevision) string {
	if r.GGUFFile == nil {
		return ""
	}
	return *r.GGUFFile
}

// ggufInclude limits a GGUF download to its file, or to its folder when it is
// one shard of several.
func ggufInclude(r verify.CatalogRevision) []string {
	f := ggufOf(r)
	if f == "" {
		return nil
	}
	if dir := path.Dir(f); dir != "." && ggufShard.MatchString(f) {
		return []string{dir + "/*"}
	}
	return []string{f}
}

// startRevision is ensureRevision with the client's own catalog, hub cache,
// hf venv and config file. configPath empty: the choice is not saved.
func startRevision(configPath string, out io.Writer) func(ctx context.Context, cfg *config.Config) error {
	return func(ctx context.Context, cfg *config.Config) error {
		hub, err := verify.DefaultHFHubCache()
		if err != nil {
			return fmt.Errorf("finding the Hugging Face cache: %w", err)
		}
		return ensureRevision(ctx, cfg, revisionDeps{
			Catalog: func(ctx context.Context) (verify.CatalogEntry, bool, error) {
				c := verify.NewCatalog(cfg.Provider.URL, cfg.PlatformType())
				if err := c.Refresh(ctx); err != nil {
					return verify.CatalogEntry{}, false, err
				}
				e, ok := c.Get(cfg.Provider.Model)
				return e, ok, nil
			},
			HubCache: hub,
			Download: func(ctx context.Context, repo, sha string, include []string) error {
				uv, err := engine.EnsureUV(ctx, out)
				if err != nil {
					return err
				}
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				hf, err := hfget.Venv{Dir: filepath.Join(home, ".venv-hf"), UV: uv, Run: hfget.ExecRunner, Out: out}.Ensure(ctx)
				if err != nil {
					return err
				}
				return hfget.Downloader{HF: hf, Run: hfget.ExecRunner, Out: out}.Download(ctx, repo, sha, include)
			},
			Save: func(cfg *config.Config) error {
				if configPath == "" {
					return nil
				}
				return config.Save(configPath, cfg)
			},
			Out: out,
		})
	}
}
