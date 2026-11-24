package setup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/hfget"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// modelDeps are what `flockadoo model` needs from the outside (model revisions C2).
type modelDeps struct {
	Config *config.Config
	// Entry returns alias's catalog entry for the configured engine; ok is
	// false when the catalog does not list it.
	Entry    func(ctx context.Context, alias string) (entry verify.CatalogEntry, ok bool, err error)
	HubCache string
	Free     hfget.FreeFunc
	Download func(ctx context.Context, repo, sha string, include []string) error
	Login    func(ctx context.Context) error
	Save     func(cfg *config.Config) error
	// AsRoot is true when the client runs as root and HF_HUB_CACHE is unset:
	// a download would land in root's home, where the client never looks.
	AsRoot bool
	Out    io.Writer
	// Entries is the whole catalog for the configured engine (model list).
	Entries func(ctx context.Context) ([]verify.CatalogEntry, error)
	// Verdicts is the server's last verdict per snapshot.
	Verdicts *verify.VerdictLog
	// Verify measures alias's snapshot at sha and asks the server.
	Verify func(ctx context.Context, alias, sha string) (verify.Status, error)
}

const modelUsage = `Usage: flockadoo model <command>

  download [alias] [--revision <sha>] [--skip-space-check]
        Download a model into the Hugging Face cache at a commit the catalog
        lists: by default the configured model at its current commit.
  login
        Log in to Hugging Face (needed only for gated models). Hugging Face
        keeps the token; Flockadoo never reads or stores it.
`

// RunModel runs `flockadoo model` with the client's own catalog, cache, hf
// venv and config file.
func RunModel(ctx context.Context, args []string, s Streams) error {
	fs := flag.NewFlagSet("model", flag.ContinueOnError)
	fs.SetOutput(s.Err)
	configPath := fs.String("config", "", "Path to configuration file")
	fs.Usage = func() { _, _ = fmt.Fprint(s.Err, modelUsage) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	path := *configPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	hub, err := verify.DefaultHFHubCache()
	if err != nil {
		return fmt.Errorf("finding the Hugging Face cache: %w", err)
	}
	return runModel(ctx, fs.Args(), modelDeps{
		Config: cfg,
		Entry: func(ctx context.Context, alias string) (verify.CatalogEntry, bool, error) {
			c := verify.NewCatalog(cfg.Provider.URL, cfg.PlatformType())
			if err := c.Refresh(ctx); err != nil {
				return verify.CatalogEntry{}, false, err
			}
			e, ok := c.Get(alias)
			return e, ok, nil
		},
		HubCache: hub,
		Free:     hfget.FreeBytes,
		Download: hfDownload(s.Out),
		Login: func(ctx context.Context) error {
			hf, err := hfProgram(ctx, s.Out)
			if err != nil {
				return err
			}
			return hfget.Login(ctx, hf, hfget.ExecInteractive, s.In, s.Out, s.Err)
		},
		Save:   func(cfg *config.Config) error { return config.Save(path, cfg) },
		AsRoot: os.Geteuid() == 0 && strings.TrimSpace(os.Getenv("HF_HUB_CACHE")) == "", //nolint:forbidigo // the refusal is about where hf would write, which this variable decides
		Out:    s.Out,
	})
}

func runModel(ctx context.Context, args []string, d modelDeps) error {
	if len(args) == 0 {
		return errors.New("name a command: download or login (flockadoo model --help)")
	}
	switch args[0] {
	case "download":
		return modelDownload(ctx, args[1:], d)
	case "login":
		if len(args) > 1 {
			return fmt.Errorf("login takes no arguments: %s", strings.Join(args[1:], " "))
		}
		return d.Login(ctx)
	default:
		return fmt.Errorf("unknown command %q: download or login (flockadoo model --help)", args[0])
	}
}

func modelDownload(ctx context.Context, args []string, d modelDeps) error {
	fs := flag.NewFlagSet("model download", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	revision := fs.String("revision", "", "a live commit of the model (default: its current one)")
	skipSpace := fs.Bool("skip-space-check", false, "download even when the disk looks too small")
	positional, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = fmt.Fprint(d.Out, modelUsage)
		return nil
	}
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return fmt.Errorf("name one model, not %d: %s", len(positional), strings.Join(positional, " "))
	}
	cfg := d.Config
	alias := cfg.Provider.Model
	if len(positional) == 1 {
		alias = positional[0]
	}
	if alias == "" {
		return errors.New("name a model: flockadoo model download <alias>")
	}
	if d.AsRoot {
		return errors.New("running as root, the model would go into root's home, where flockadoo never looks; run it as the user that runs flockadoo, or set HF_HUB_CACHE")
	}
	sha := strings.ToLower(strings.TrimSpace(*revision))
	if sha != "" && !fullSHA.MatchString(sha) {
		return fmt.Errorf("--revision %s: give the full 40-character commit", *revision)
	}

	entry, ok, err := d.Entry(ctx, alias)
	if err != nil {
		return fmt.Errorf("reading the model catalog: %w", err)
	}
	service := config.PlatformType(cfg.Provider.Engine)
	if !ok {
		return fmt.Errorf("%s is not in the catalog for %s", alias, service)
	}
	rev, err := pickRevision(entry, service, sha)
	if err != nil {
		return err
	}

	if revisionCached(d.HubCache, rev.HFRepo, rev.HFRevision, ggufOf(rev)) {
		_, _ = fmt.Fprintf(d.Out, "%s at %s (%s) is already in %s.\n", rev.HFRepo, rev.HFRevision, rev.Status, d.HubCache)
	} else {
		if rev.Gated {
			_, _ = fmt.Fprintf(d.Out, "%s is gated: Hugging Face needs its licence accepted and a login.\n", rev.HFRepo)
		}
		if !*skipSpace {
			if err := hfget.CheckSpace(d.HubCache, rev.SizeBytes, d.Free); err != nil {
				return fmt.Errorf("%w, or pass --skip-space-check", err)
			}
		}
		_, _ = fmt.Fprintf(d.Out, "Downloading %s at %s (%s) into %s.\n", rev.HFRepo, rev.HFRevision, rev.Status, d.HubCache)
		if err := d.Download(ctx, rev.HFRepo, rev.HFRevision, ggufInclude(rev)); err != nil {
			if errors.Is(err, hfget.ErrGated) {
				explainGated(d.Out, rev.HFRepo)
			}
			return err
		}
	}

	if alias != cfg.Provider.Model {
		_, _ = fmt.Fprintf(d.Out, "Done. Your configured model is %s; this download does not change it.\n", cfg.Provider.Model)
		return nil
	}
	if cfg.Provider.HFRevision != rev.HFRevision {
		cfg.Provider.HFRevision = rev.HFRevision
		cfg.Provider.HFRepo = rev.HFRepo
		if f := ggufOf(rev); f != "" {
			cfg.Provider.GGUFFile = f
		}
		if err := d.Save(cfg); err != nil {
			return fmt.Errorf("remembering the commit to serve: %w", err)
		}
	}
	_, _ = fmt.Fprintf(d.Out, "Done. Flockadoo serves %s at %s from its next start; restart it if it is running.\n", alias, rev.HFRevision)
	return nil
}

// pickRevision is the live revision sha of entry for service, or its current
// one when sha is empty.
func pickRevision(entry verify.CatalogEntry, service, sha string) (verify.CatalogRevision, error) {
	var current *verify.CatalogRevision
	for i, r := range entry.Revisions {
		if r.ServiceType != service {
			continue
		}
		if sha != "" && r.HFRevision == sha {
			return r, nil
		}
		if r.Status == "current" {
			current = &entry.Revisions[i]
		}
	}
	if sha != "" {
		return verify.CatalogRevision{}, fmt.Errorf("%s is not a live revision of %s for %s; the catalog lists only the current and accepted ones", sha, entry.Alias, service)
	}
	if current == nil {
		return verify.CatalogRevision{}, fmt.Errorf("the catalog names no commit of %s for %s (a server before model revisions?)", entry.Alias, service)
	}
	return *current, nil
}

// explainGated walks the operator through getting access to a gated repo
// (decision D5). The token goes from them to hf, never through Flockadoo.
func explainGated(out io.Writer, repo string) {
	_, _ = fmt.Fprintf(out, `Hugging Face refused the download: %[1]s is gated. To get access:
  1. Create a Hugging Face account at https://huggingface.co/join, if you have none.
  2. Open https://huggingface.co/%[1]s and accept its licence. Some publishers approve requests by hand, so this can take a while.
  3. Create a read-only token at https://huggingface.co/settings/tokens.
  4. Run `+"`flockadoo model login`"+` and paste the token at Hugging Face's prompt.
Then run this command again.
`, repo)
}

// parseInterleaved parses flags that may come before or after the positional
// arguments, and returns the positional ones.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
