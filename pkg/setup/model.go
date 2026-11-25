package setup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

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
  list
        Show the models in the Hugging Face cache that the catalog lists: each
        commit, its size, its status (current, accepted or not live) and the
        server's last verdict on it. Nothing is hashed.
  verify [alias] [--revision <sha>]
        Measure a downloaded model now and ask the server whether it matches
        (by default the configured model at the commit it serves).
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
	config.ApplyEnvOverrides(cfg)
	hub, warning, err := HubCacheNotice(cfg)
	if err != nil {
		return err
	}
	if warning != "" {
		_, _ = fmt.Fprintln(s.Err, "Warning: "+warning)
	}
	catalog := verify.NewCatalog(cfg.Provider.URL, cfg.PlatformType())
	loaded := false
	load := func(ctx context.Context) error {
		if loaded {
			return nil
		}
		if err := catalog.Refresh(ctx); err != nil {
			return err
		}
		loaded = true
		return nil
	}
	verdicts := VerdictLog(path)
	return runModel(ctx, fs.Args(), modelDeps{
		Config: cfg,
		Entry: func(ctx context.Context, alias string) (verify.CatalogEntry, bool, error) {
			if err := load(ctx); err != nil {
				return verify.CatalogEntry{}, false, err
			}
			e, ok := catalog.Get(alias)
			return e, ok, nil
		},
		Entries: func(ctx context.Context) ([]verify.CatalogEntry, error) {
			if err := load(ctx); err != nil {
				return nil, err
			}
			var out []verify.CatalogEntry
			for _, a := range catalog.Aliases() {
				if e, ok := catalog.Get(a); ok {
					out = append(out, e)
				}
			}
			return out, nil
		},
		Verdicts: verdicts,
		Verify: func(ctx context.Context, alias, sha string) (verify.Status, error) {
			if !cfg.HasAPIKey() {
				return "", errors.New("no provider API key in the config; run `flockadoo setup`")
			}
			if err := load(ctx); err != nil {
				return "", err
			}
			v := verify.NewVerifier(catalog, verify.NewServerClient(cfg.Provider.URL, cfg.Provider.APIKey), cfg.PlatformType(), hub, "")
			v.SetRevision(sha)
			v.SetVerdictLog(verdicts)
			return v.Check(ctx, alias)
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

// VerdictLog is the file, next to the config, where the client keeps the
// server's last verdict on each snapshot (model revisions C2).
func VerdictLog(configPath string) *verify.VerdictLog {
	return verify.NewVerdictLog(filepath.Join(filepath.Dir(configPath), "model-verdicts.json"))
}

func runModel(ctx context.Context, args []string, d modelDeps) error {
	if len(args) == 0 {
		return errors.New("name a command: download, list, verify or login (flockadoo model --help)")
	}
	switch args[0] {
	case "download":
		return modelDownload(ctx, args[1:], d)
	case "list":
		if len(args) > 1 {
			return fmt.Errorf("list takes no arguments: %s", strings.Join(args[1:], " "))
		}
		return modelList(ctx, d)
	case "verify":
		return modelVerify(ctx, args[1:], d)
	case "login":
		if len(args) > 1 {
			return fmt.Errorf("login takes no arguments: %s", strings.Join(args[1:], " "))
		}
		return d.Login(ctx)
	default:
		return fmt.Errorf("unknown command %q: download, list, verify or login (flockadoo model --help)", args[0])
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

// modelList shows each catalog alias's cached snapshots without hashing
// anything: a GGUF is hashed in full, so measuring them all could take
// minutes. A snapshot's status is read from its folder name, so it is a
// claim, not a check; the verdict is the server's last answer on it.
func modelList(ctx context.Context, d modelDeps) error {
	entries, err := d.Entries(ctx)
	if err != nil {
		return fmt.Errorf("reading the model catalog: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	service := config.PlatformType(d.Config.Provider.Engine)
	tw := tabwriter.NewWriter(d.Out, 0, 0, 2, ' ', 0)
	rows := 0
	for _, e := range entries {
		for _, repo := range revisionRepos(e, service) {
			for _, sha := range cachedCommits(d.HubCache, repo) {
				if rows == 0 {
					_, _ = fmt.Fprintln(tw, "MODEL\tCOMMIT\tSTATUS\tSIZE\tLAST VERDICT")
				}
				rows++
				status := "not live (retired, or never approved)"
				if r, ok := liveRevision(e, service, sha); ok {
					status = r.Status
				}
				size := snapshotSize(filepath.Join(d.HubCache, verify.HFRepoToCacheDir(repo), "snapshots", sha))
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Alias, sha, status, humanBytes(size), lastVerdict(d.Verdicts, e.Alias, service, sha))
			}
		}
	}
	if rows == 0 {
		_, _ = fmt.Fprintf(d.Out, "No model the catalog lists for %s is in %s. Download one with `flockadoo model download`.\n", service, d.HubCache)
		return nil
	}
	return tw.Flush()
}

// modelVerify measures one cached snapshot now and shows the server's verdict.
// The client has no hashes to compare with: the verdict is the server's.
func modelVerify(ctx context.Context, args []string, d modelDeps) error {
	fs := flag.NewFlagSet("model verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	revision := fs.String("revision", "", "the commit to measure (default: the one served)")
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
		return errors.New("name a model: flockadoo model verify <alias>")
	}
	entry, ok, err := d.Entry(ctx, alias)
	if err != nil {
		return fmt.Errorf("reading the model catalog: %w", err)
	}
	service := config.PlatformType(cfg.Provider.Engine)
	if !ok {
		return fmt.Errorf("%s is not in the catalog for %s", alias, service)
	}

	sha := strings.ToLower(strings.TrimSpace(*revision))
	if sha == "" && alias == cfg.Provider.Model {
		sha = cfg.Provider.HFRevision
	}
	repo := firstNonEmpty(cfg.Provider.HFRepo, alias)
	if alias != cfg.Provider.Model {
		repo = alias
	}
	if r, ok := liveRevision(entry, service, sha); ok {
		repo = r.HFRepo
	}
	if sha == "" {
		// The served commit is not known: the cached live one, current first.
		for _, r := range entry.Revisions {
			if r.ServiceType == service && revisionCached(d.HubCache, r.HFRepo, r.HFRevision, ggufOf(r)) && (sha == "" || r.Status == "current") {
				sha, repo = r.HFRevision, r.HFRepo
			}
		}
	}
	if sha == "" || !snapshotExists(d.HubCache, repo, sha) {
		what := alias
		if sha != "" {
			what += " at " + sha
		}
		return fmt.Errorf("%s is not in %s; download it with `flockadoo model download`", what, d.HubCache)
	}

	_, _ = fmt.Fprintf(d.Out, "Measuring %s at %s and asking the server...\n", alias, sha)
	status, err := d.Verify(ctx, alias, sha)
	if err != nil {
		return err
	}
	live, isLive := liveRevision(entry, service, sha)
	switch {
	case status == verify.StatusVerified && isLive && live.Status == "current":
		_, _ = fmt.Fprintln(d.Out, "verified (current)")
	case status == verify.StatusVerified && isLive && live.Status == "accepted":
		_, _ = fmt.Fprintln(d.Out, "verified (accepted: the next approved revision retires it)")
	case status == verify.StatusVerified:
		_, _ = fmt.Fprintln(d.Out, "verified")
	case status == verify.StatusFailed:
		_, _ = fmt.Fprintln(d.Out, "does not match: these files are not the approved ones")
		return fmt.Errorf("%s at %s does not match the approved files", alias, sha)
	default:
		_, _ = fmt.Fprintf(d.Out, "not verified: the server says %q\n", status)
		return fmt.Errorf("%s at %s is not verified (%s)", alias, sha, status)
	}
	return nil
}

// revisionRepos are the repos entry's revisions for service come from.
func revisionRepos(e verify.CatalogEntry, service string) []string {
	var repos []string
	for _, r := range e.Revisions {
		if r.ServiceType == service && !slices.Contains(repos, r.HFRepo) {
			repos = append(repos, r.HFRepo)
		}
	}
	if len(repos) == 0 && e.HFRepo != nil && *e.HFRepo != "" {
		repos = append(repos, *e.HFRepo)
	}
	return repos
}

// liveRevision is entry's live revision sha for service.
func liveRevision(e verify.CatalogEntry, service, sha string) (verify.CatalogRevision, bool) {
	if sha == "" {
		return verify.CatalogRevision{}, false
	}
	for _, r := range e.Revisions {
		if r.ServiceType == service && r.HFRevision == sha {
			return r, true
		}
	}
	return verify.CatalogRevision{}, false
}

// cachedCommits are the snapshot folders of repo in hub, sorted.
func cachedCommits(hub, repo string) []string {
	dirs, err := os.ReadDir(filepath.Join(hub, verify.HFRepoToCacheDir(repo), "snapshots"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range dirs {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func snapshotExists(hub, repo, sha string) bool {
	st, err := os.Stat(filepath.Join(hub, verify.HFRepoToCacheDir(repo), "snapshots", sha))
	return err == nil && st.IsDir()
}

// snapshotSize adds up the files of a snapshot. In the hub cache they are
// links to blobs, so each is stat'ed through its link. Nothing is opened.
func snapshotSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is left out of the size, not a reason to stop listing
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			total += st.Size()
		}
		return nil
	})
	return total
}

func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
}

// lastVerdict is the server's last answer on a snapshot, for model list.
func lastVerdict(l *verify.VerdictLog, alias, service, sha string) string {
	if l == nil {
		return "never verified"
	}
	r, ok := l.Last(alias, service, sha)
	if !ok {
		return "never verified"
	}
	at := r.At.Local().Format(time.DateOnly + " 15:04")
	switch r.Status {
	case verify.StatusVerified:
		return "verified " + at
	case verify.StatusFailed:
		return "does not match " + at
	default:
		return string(r.Status) + " " + at
	}
}
