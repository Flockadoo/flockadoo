package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// EnsureOnStart checks the configured inference engine before the dashboard.
// Interactive TTYs prompt to start it when it is down; otherwise auto_start
// decides. A spinner stays on stdout until the engine is serving.
func EnsureOnStart(ctx context.Context, cfg *config.Config, io Streams) error {
	_, err := EnsureOnStartAt(ctx, cfg, "", io)
	return err
}

// EnsureOnStartAt is EnsureOnStart that also picks, downloads and remembers
// the commit to serve before it starts an engine (model revisions C4),
// saving the choice to the config file at configPath.
// The report says what the start found that the dashboard shows.
func EnsureOnStartAt(ctx context.Context, cfg *config.Config, configPath string, io Streams) (StartReport, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.DefaultDownloadTimeout)
	defer cancel()
	out := io.Out
	if out == nil {
		out = os.Stdout
	}
	var report StartReport
	scan := func(ctx context.Context) ([]engine.Orphan, error) {
		found, err := scanOrphans(ctx)
		report.Orphans = append(report.Orphans, found...)
		return found, err
	}
	err := ensureOnStartWith(ctx, cfg, io, interactiveReader(io.In), startRevision(configPath, out), scan, nil)
	return report, err
}

func interactiveReader(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && isTTY(f)
}

func ensureOnStart(ctx context.Context, cfg *config.Config, io Streams, interactive bool) error {
	return ensureOnStartWith(ctx, cfg, io, interactive, nil, nil, nil)
}

// ensureOnStartWith runs revision, when set, just before it starts an engine,
// and scan (scanOrphans when nil) before that.
// What the start did that the dashboard shows goes into report, when set.
func ensureOnStartWith(ctx context.Context, cfg *config.Config, io Streams, interactive bool, revision func(context.Context, *config.Config) error, scan func(context.Context) ([]engine.Orphan, error), report *StartReport) error {
	if scan == nil {
		scan = scanOrphans
	}
	if cfg == nil {
		return nil
	}
	if io.Out == nil {
		io.Out = os.Stdout
	}
	if io.Err == nil {
		io.Err = os.Stderr
	}
	if io.In == nil {
		io.In = os.Stdin
	}

	if err := refreshFlock(ctx, cfg); err != nil {
		return err
	}

	kind, ok := engine.ParseKind(cfg.Provider.Engine)
	if !ok {
		return nil
	}
	label := engine.Label(kind)
	url := cfg.Provider.LLMURL
	if url == "" {
		url = engine.DefaultURL(kind)
	}

	// A flock member downloads the flock's commit (model revisions F1)
	// before it shows the command the operator starts the engine with.
	if role := cfg.Provider.FlockRole; (role == "worker" || role == "master") && revision != nil && cfg.Provider.HFRevision != "" {
		if err := revision(ctx, cfg); err != nil {
			return err
		}
	}
	switch cfg.Provider.FlockRole {
	case "worker":
		return observeWorker(io, kind, cfg)
	case "master":
		return observeMaster(ctx, io, kind, label, url, cfg)
	}

	if engine.Healthy(ctx, kind, url) {
		_, _ = fmt.Fprintf(io.Out, "%s is running at %s.\n", label, url)
		return nil
	}

	join := engine.MasterJoinAddr(cfg.Provider.EngineArgs)
	if join != "" && !engine.PortOpen(ctx, url) && engine.PortOpen(ctx, "http://"+join) {
		joined, need := engine.WorkerSlots(onlinePeers(ctx), engine.NNodes(cfg.Provider.EngineArgs))
		_, _ = fmt.Fprintf(io.Out, "Waiting for workers to join (%d/%d).\n", joined, need)
		return nil
	}

	waitMsg := fmt.Sprintf("Waiting for %s to start serving at %s", label, url)
	if join != "" && !engine.PortOpen(ctx, url) {
		waitMsg = fmt.Sprintf("Waiting for the master to accept other nodes at tcp://%s", join)
	}
	if engine.PortOpen(ctx, url) {
		_, _ = fmt.Fprintf(io.Out, "%s is already starting at %s.\n", label, url)
		err := SpinWhile(io.Out, waitMsg, func() error {
			return engine.WaitHealthy(ctx, kind, url, 2*time.Second, nil)
		})
		if err != nil {
			return fmt.Errorf("%w (see %s)", err, engine.LogPath(cfg.Logging.LogDir))
		}
		_, _ = fmt.Fprintf(io.Out, "%s is ready at %s.\n", label, url)
		return nil
	}

	bin := engine.ResolveBin(kind, cfg.Provider.EngineBin)
	if bin == "" {
		detected := engine.Detect(kind)
		if detected.Unusable != "" {
			_, _ = fmt.Fprintf(io.Out, "%s at %s cannot run (%s).\n", label, detected.Bin, detected.Unusable)
			_, _ = fmt.Fprintln(io.Out, "Fix it with: flockadoo setup")
			return nil
		}
		_, _ = fmt.Fprintf(io.Out, "%s is not installed and is not running at %s.\n", label, url)
		_, _ = fmt.Fprintln(io.Out, "Install it with: flockadoo setup")
		return nil
	}

	startNow := cfg.Provider.AutoStart
	if interactive {
		ok, err := promptYes(io.In, io.Out, fmt.Sprintf("%s is not running at %s. Start it now?", label, url))
		if err != nil {
			return err
		}
		startNow = ok
	}
	if !startNow {
		_, _ = fmt.Fprintf(io.Out, "Skipping %s start. Inference will fail until it is serving at %s.\n", label, url)
		return nil
	}

	found := reportOrphans(ctx, io.Out, scan)
	// A model that does not fit the memory free now is not started (E3).
	if err := checkStartFit(ctx, cfg, found); err != nil {
		return err
	}
	err := SpinWhile(io.Out, waitMsg, func() error {
		if revision != nil {
			if err := revision(ctx, cfg); err != nil {
				return err
			}
		}
		return engine.StartAndWait(ctx, cfg, cfg.Logging.LogDir)
	})
	if err != nil {
		return fmt.Errorf("%w (see %s)", err, engine.LogPath(cfg.Logging.LogDir))
	}
	if join != "" && !engine.PortOpen(ctx, url) {
		joined, need := engine.WorkerSlots(onlinePeers(ctx), engine.NNodes(cfg.Provider.EngineArgs))
		_, _ = fmt.Fprintf(io.Out, "Waiting for workers to join (%d/%d).\n", joined, need)
		return nil
	}
	_, _ = fmt.Fprintf(io.Out, "%s is ready at %s.\n", label, url)
	return nil
}

func observeWorker(io Streams, kind engine.Kind, cfg *config.Config) error {
	if engine.WorkerAlive(kind, cfg.Provider.EngineArgs) {
		_, _ = fmt.Fprintln(io.Out, "Worker is running. It joins the master and does not serve HTTP.")
		return nil
	}
	printSavedCommand(io.Out, cfg, docsFlockWorker)
	_, _ = fmt.Fprintln(io.Out, "Start it in another terminal. This client keeps running.")
	return nil
}

func observeMaster(ctx context.Context, io Streams, kind engine.Kind, label, url string, cfg *config.Config) error {
	switch state := engine.MasterState(ctx, kind, url, cfg.Provider.EngineArgs, onlinePeers(ctx)); state {
	case "ready":
		_, _ = fmt.Fprintf(io.Out, "%s is ready at %s.\n", label, url)
	case "not started":
		printSavedCommand(io.Out, cfg, docsFlockMaster)
		_, _ = fmt.Fprintln(io.Out, "Start it in another terminal. This client keeps running.")
	default:
		_, _ = fmt.Fprintln(io.Out, state)
	}
	return nil
}

func printSavedCommand(out io.Writer, cfg *config.Config, docs string) {
	spec, err := engine.SpecFromConfig(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(out, docs)
		return
	}
	_, _ = fmt.Fprintln(out, spec.Display())
	_, _ = fmt.Fprintln(out, docs)
}
