package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/gpu"
	"github.com/Flockadoo/flockadoo/pkg/verify"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
)

// hostGOOS is runtime.GOOS; tests override it to cover Windows/macOS remap.
var hostGOOS = runtime.GOOS //nolint:forbidigo // setup's one read of the platform; tests override hostGOOS

// detectEngine and installEngine are replaced in tests.
var (
	detectEngine  = engine.Detect
	installEngine = engine.Install
	startDetached = engine.StartDetached
)

// Execute runs the setup wizard.
func Execute(opts Options, io Streams) error {
	if io.Out == nil {
		io.Out = os.Stdout
	}
	if io.Err == nil {
		io.Err = os.Stderr
	}

	path := opts.ConfigPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}

	cfg, err := loadOrFresh(path)
	if err != nil {
		return err
	}
	prevModel, prevEngine := cfg.Provider.Model, cfg.Provider.Engine

	platformURL := config.ResolvePlatformURL(opts.CatalogURL, cfg.Provider.URL)
	cfg.Provider.URL = platformURL
	if opts.Port > 0 {
		cfg.Server.Port = opts.Port
	}
	// An engine the user runs is theirs to install and start.
	external := strings.TrimSpace(opts.LLMURL) != ""
	if external {
		opts.NoStart = true
	}

	winglink.WithWait = func(msg string, fn func() error) error {
		return SpinWhile(io.Out, msg, fn)
	}
	_, _ = fmt.Fprintln(io.Out, "=== Flockadoo Client Setup ===")
	_, _ = fmt.Fprintf(io.Out, "Config: %s\n", path)
	_, _ = fmt.Fprintf(io.Out, "API:    %s\n\n", platformURL)

	if err := applyAPIKey(cfg, opts, io); err != nil {
		return err
	}
	var plan flockSettings
	var planErr error
	if err := SpinWhile(io.Out, "Reading flock settings", func() error {
		plan, planErr = flockPlan(cfg)
		return nil
	}); err != nil {
		return err
	}
	if planErr != nil && plan.Role != "single" {
		return planErr
	}

	var kind engine.Kind
	if plan.Role == "master" || plan.Role == "worker" {
		kind, err = resolveEngine(hostGOOS, firstNonEmpty(plan.ServiceType, "vllm"))
		if err != nil {
			return err
		}
		printFlock(io.Out, plan)
		if plan.Role == "worker" {
			if msg := mixedGPUWarning(localGPUProduct(), plan.MasterGPU); msg != "" {
				_, _ = fmt.Fprintln(io.Err, msg)
			}
		}
	} else {
		kind, err = applyEngine(cfg, opts, io)
		if err != nil {
			return err
		}
	}

	detected := detectEngine(kind)
	autoStart := true
	pinned := ""
	if kind == engine.KindVLLM {
		pinned = strings.TrimSpace(plan.VLLMVersion)
	}
	// First-time workers must wait for the master's pin. An existing vLLM
	// install is not a pin — joining with it starts a mixed-version flock.
	// Enrolled re-runs keep going when the engine route omits the version
	// (transient fetch miss); those machines already joined.
	if plan.Role == "worker" && kind == engine.KindVLLM && pinned == "" && !cfg.EnrolledWorker() {
		return fmt.Errorf("this flock has no vLLM version yet. Run setup on the master first")
	}
	// Every machine installs the pin, and the client refuses a release below
	// the minimum, so such a pin would stop the whole flock.
	if pinned != "" && engine.TooOld(kind, pinned) {
		return fmt.Errorf("this flock pins vLLM %s, and Flockadoo needs vLLM %s or newer. Ask the flock's owner to change the vLLM version on the Nodes page", pinned, engine.MinVLLMVersion)
	}
	// unsupported is an installed vLLM older than Flockadoo supports that is
	// still in place: it is never started.
	unsupported := false
	if detected.Found && (kind == engine.KindVLLM || kind == engine.KindVLLMMetal) && (!external || plan.Role == "worker") {
		have := engine.InstalledVersion(context.Background(), detected.Bin) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1); InstalledVersion bounds the call itself
		var notice, question, kept, want string
		switch {
		case pinned != "" && have != "" && have != pinned:
			question = fmt.Sprintf("vLLM %s is installed. This flock uses vLLM %s. Reinstall it now?", have, pinned)
			kept = fmt.Sprintf("Leaving vLLM %s installed. This flock uses vLLM %s, so it may not start.", have, pinned)
			want = pinned
		case engine.TooOld(kind, have):
			unsupported = true
			notice = fmt.Sprintf("%s %s is installed. Flockadoo needs vLLM %s or newer.", engine.Label(kind), have, engine.MinVLLMVersion)
			question = "Reinstall it now?"
			kept = fmt.Sprintf("Leaving %s %s installed. The client will not serve through it. Run flockadoo setup again to install a supported release.", engine.Label(kind), have)
		}
		if question != "" {
			if notice != "" {
				_, _ = fmt.Fprintln(io.Out, notice)
			}
			reinstall := opts.Yes || plan.Role == "worker"
			if !reinstall {
				ok, err := promptYes(io.In, io.Out, question)
				if err != nil {
					return err
				}
				reinstall = ok
			}
			if reinstall {
				release := "vLLM " + want
				if want == "" {
					release = "the current " + engine.Label(kind) + " release"
				}
				_, _ = fmt.Fprintf(io.Out, "Installing %s...\n", release)
				if err := installEngine(context.Background(), kind, want, io.Out); err != nil { //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
					if plan.Role == "worker" {
						// Exchange spends the join code. Do not enroll on the
						// wrong (or now-missing) vLLM after a failed reinstall.
						return fmt.Errorf("install %s: %w", release, err)
					}
					_, _ = fmt.Fprintf(io.Err, "install failed: %v\n", err)
					autoStart = false
				} else {
					detected = detectEngine(kind)
					unsupported = false
				}
			} else {
				autoStart = false
				_, _ = fmt.Fprintln(io.Err, kept)
			}
		}
	}
	if !detected.Found && external && plan.Role != "worker" {
		autoStart = false
	} else if !detected.Found {
		doInstall := opts.Install || (plan.Role == "worker")
		if !doInstall && !opts.Yes {
			q := fmt.Sprintf("%s is not installed. Install it now?", engine.Label(kind))
			if detected.Unusable != "" {
				q = fmt.Sprintf("%s at %s cannot run (%s). Reinstall it now?", engine.Label(kind), detected.Bin, detected.Unusable)
			}
			ok, err := promptYes(io.In, io.Out, q)
			if err != nil {
				return err
			}
			doInstall = ok
		}
		if doInstall {
			_, _ = fmt.Fprintf(io.Out, "Installing %s...\n", engine.Label(kind))
			if err := installEngine(context.Background(), kind, pinned, io.Out); err != nil { //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
				if plan.Role == "worker" {
					return fmt.Errorf("install %s: %w", engine.Label(kind), err)
				}
				_, _ = fmt.Fprintf(io.Err, "install failed: %v\n", err)
				autoStart = false
			} else {
				detected = detectEngine(kind)
			}
		} else {
			autoStart = false
			_, _ = fmt.Fprintf(io.Out, "Skipping install. You can install %s later and re-run flockadoo setup.\n", kind)
		}
	}
	if detected.Found {
		_, _ = fmt.Fprintf(io.Out, "Found %s at %s\n", kind, detected.Bin)
	}
	bin := engine.BinPath(kind, firstNonEmpty(detected.Bin, cfg.Provider.EngineBin))
	if filepath.IsAbs(bin) {
		cfg.Provider.EngineBin = bin
	} else if !detected.Found {
		cfg.Provider.EngineBin = ""
	}

	var entry verify.CatalogEntry
	if plan.Role == "master" || plan.Role == "worker" {
		entry = flockEntry(opts, kind, platformURL, plan, io)
		cfg.Provider.Model = entry.Alias
	} else {
		entry, err = applyModel(cfg, opts, kind, platformURL, io)
		if err != nil {
			return err
		}
	}

	cfg.Provider.Engine = string(kind)
	cfg.Provider.LLMURL = engine.DefaultURL(kind)
	if external {
		cfg.Provider.LLMURL = strings.TrimSpace(opts.LLMURL)
	}
	cfg.Provider.AutoStart = autoStart && !external && detected.Found && !unsupported && plan.Role != "master" && plan.Role != "worker"

	hfRepo := engine.HFRepo(entry)
	serveOpts := engine.ServeOptsFromCatalog(entry)
	if kind != engine.KindLlamaCpp {
		if err := serveOpts.Validate(); err != nil {
			return fmt.Errorf("the catalog's settings for %s cannot start vLLM: %w. Pick another model, or ask Flockadoo to fix this one", entry.Alias, err)
		}
	}
	applyServeOpts(cfg, entry, hfRepo)
	// Model revisions C6: a wizard that will start the engine pins the
	// commit first and starts on it; the download runs once the operator
	// agrees to start. Without a start there is nothing to pin yet: the
	// client pins and downloads at its own start (C4).
	var download func(context.Context) error
	willStart := plan.Role != "master" && plan.Role != "worker" && !opts.NoStart && detected.Found && !unsupported
	if willStart {
		hub, err := verify.DefaultHFHubCache()
		if err != nil {
			return fmt.Errorf("finding the Hugging Face cache: %w", err)
		}
		download, err = planRevision(context.Background(), cfg, prevModel, prevEngine, entry, hub, io) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
		if err != nil {
			return err
		}
		serveOpts.Revision, serveOpts.HubCache = cfg.Provider.HFRevision, hub
		hfRepo = firstNonEmpty(cfg.Provider.HFRepo, hfRepo)
	} else if prevModel != cfg.Provider.Model || config.PlatformType(prevEngine) != config.PlatformType(cfg.Provider.Engine) {
		cfg.Provider.HFRevision = ""
	}
	spec := engine.ServeSpec(kind, bin, entry.Alias, hfRepo, serveOpts)
	if err := prepareFlock(cfg, &spec); err != nil {
		if saveErr := config.Save(path, cfg); saveErr != nil {
			return saveErr
		}
		return err
	}
	// The node token has to be on disk before the engine starts. A crash
	// while weights download must not spend the join code with nothing saved.
	if err := config.Save(path, cfg); err != nil {
		return err
	}

	if plan.Role == "master" || plan.Role == "worker" {
		// The operator starts the flock's engine from the command below, so
		// the flock's commit (model revisions F1) is downloaded first.
		if err := downloadFlockRevision(cfg, entry, opts, io); err != nil {
			_, _ = fmt.Fprintf(io.Out, "\nWrote %s\n", path)
			return err
		}
		printFlockStart(io.Out, plan.Role, spec)
	} else if err := maybeStart(kind, spec, cfg, opts, detected.Found && !unsupported, io, download); err != nil {
		if saveErr := config.Save(path, cfg); saveErr != nil {
			return saveErr
		}
		_, _ = fmt.Fprintf(io.Out, "\nWrote %s\n", path)
		_, _ = fmt.Fprintf(io.Err, "Setup did not finish: %s is not serving yet (see %s).\n", engine.Label(kind), engine.LogPath(cfg.Logging.LogDir))
		_, _ = fmt.Fprintf(io.Err, "Leave the engine running, then re-run: flockadoo setup\n")
		return err
	}

	if err := config.Save(path, cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(io.Out, "\nWrote %s\n", path)
	_, _ = fmt.Fprintln(io.Out, "\nStart the Flockadoo client:")
	_, _ = fmt.Fprintln(io.Out, "  flockadoo")
	_, _ = fmt.Fprintf(io.Out, "  flockadoo --config %s\n", path)
	_, _ = fmt.Fprintln(io.Out, "\nRe-run this wizard anytime:")
	_, _ = fmt.Fprintln(io.Out, "  flockadoo setup")
	return nil
}

func loadOrFresh(path string) (*config.Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return config.Defaults(), nil
	}
	return config.Load(path)
}

func applyAPIKey(cfg *config.Config, opts Options, io Streams) error {
	saved := credential(cfg.Provider.APIKey)
	explicit := credential(opts.APIKey)
	if cfg.EnrolledWorker() && explicit != "" && explicit != saved {
		cfg.Provider.NodeToken = ""
		cfg.Provider.FlockRole = ""
	} else if cfg.EnrolledWorker() {
		_, _ = fmt.Fprintln(io.Out, "This machine is already in the flock.")
		return nil
	}
	key := explicit
	if key == "" {
		key = credential(config.APIKeyFromEnv())
	}
	if key == "" {
		key = credential(cfg.Provider.APIKey)
	}
	// --api-key and --yes skip the prompt. An env key is only the default,
	// so a worker can paste a join code over a provider key already in the shell.
	if opts.Yes || strings.TrimSpace(opts.APIKey) != "" {
		if key == "" || key == "your_api_key_here" {
			return fmt.Errorf("--api-key is required with --yes")
		}
		cfg.Provider.APIKey = key
		return nil
	}
	def := ""
	if key != "" && key != "your_api_key_here" {
		def = "keep current"
	}
	got, err := promptLine(io.In, io.Out, "Provider API key or join code", def)
	if err != nil && got == "" {
		return fmt.Errorf("API key is required")
	}
	if got == "" || got == "keep current" {
		if key == "" || key == "your_api_key_here" {
			return fmt.Errorf("API key is required")
		}
		cfg.Provider.APIKey = key
		return nil
	}
	cfg.Provider.APIKey = credential(got)
	return nil
}

func applyEngine(cfg *config.Config, opts Options, io Streams) (engine.Kind, error) {
	if opts.Engine != "" {
		return resolveEngine(hostGOOS, opts.Engine)
	}
	if opts.Yes {
		if cfg.Provider.Engine != "" {
			if k, err := resolveEngine(hostGOOS, cfg.Provider.Engine); err == nil {
				return k, nil
			}
		}
		return "", fmt.Errorf("--engine is required with --yes")
	}

	selectable := make([]engine.Option, 0)
	def := 1
	_, _ = fmt.Fprintln(io.Out, "Inference engine:")
	for _, o := range engine.HostOptions() {
		if o.Unavailable != "" {
			_, _ = fmt.Fprintf(io.Out, "     %s — unavailable: %s\n", o.Label, o.Unavailable)
			continue
		}
		selectable = append(selectable, o)
		n := len(selectable)
		if string(o.Kind) == cfg.Provider.Engine {
			def = n
		}
		_, _ = fmt.Fprintf(io.Out, "  %d) %s\n", n, o.Label)
	}
	if len(selectable) == 0 {
		return "", fmt.Errorf("no engines available on this machine")
	}
	choice, err := promptChoice(io.In, io.Out, len(selectable), def)
	if err != nil {
		return "", err
	}
	return selectable[choice-1].Kind, nil
}

func resolveEngine(goos, name string) (engine.Kind, error) {
	k, ok := engine.ParseKind(name)
	if !ok {
		return "", fmt.Errorf("unknown engine %q (vllm, vllm-metal, llama_cpp)", name)
	}
	return engine.RuntimeKind(goos, k), nil
}

func printFlock(out io.Writer, plan flockSettings) {
	name := firstNonEmpty(plan.Name, "this flock")
	if plan.Role == "worker" {
		_, _ = fmt.Fprintf(out, "\nJoining Team Flock %s\n", name)
	} else {
		_, _ = fmt.Fprintf(out, "\nTeam Flock %s\n", name)
	}
	_, _ = fmt.Fprintf(out, "Model: %s\n", modelLabel(plan))
	if plan.VLLMVersion != "" {
		_, _ = fmt.Fprintf(out, "vLLM: %s\n", plan.VLLMVersion)
	}
}

func mixedGPUWarning(local, master string) string {
	local = strings.TrimSpace(local)
	master = strings.TrimSpace(master)
	if local == "" || master == "" || local == master {
		return ""
	}
	return fmt.Sprintf("This machine has %s. The master has %s. Mixed GPUs are untested on Flockadoo and the flock may not start.", local, master)
}

var localGPUProduct = readGPUProduct

func readGPUProduct() string {
	mon, err := gpu.NewMonitor()
	if err != nil {
		return ""
	}
	info, err := mon.GetGPUInfo(context.Background()) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	if err != nil || info == nil || info.ProductName == "" || info.ProductName == "Unknown" {
		return ""
	}
	return info.ProductName
}

func localGPU(ctx context.Context) *gpu.GPUInfo {
	mon, err := gpu.NewMonitor()
	if err != nil {
		return nil
	}
	info, err := mon.GetGPUInfo(ctx)
	if err != nil || info == nil || info.ProductName == "" || info.ProductName == "Unknown" {
		return nil
	}
	return info
}

func modelLabel(plan flockSettings) string {
	name := strings.TrimSpace(plan.DisplayName)
	alias := strings.TrimSpace(plan.Model)
	if name == "" || strings.EqualFold(name, alias) {
		return alias
	}
	return name + " (" + alias + ")"
}

func flockEntry(opts Options, kind engine.Kind, platformURL string, plan flockSettings, io Streams) verify.CatalogEntry {
	fallback := verify.CatalogEntry{Alias: plan.Model, ServiceType: "vllm"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	defer cancel()
	var entries []verify.CatalogEntry
	var err error
	if opts.OfflineCatalog != "" {
		entries, err = compat.LoadOfflineCatalog(opts.OfflineCatalog, engine.CatalogType(kind))
	} else {
		err = SpinWhile(io.Out, "Loading the flock's model", func() error {
			var fetchErr error
			entries, fetchErr = compat.FetchCatalog(ctx, platformURL, engine.CatalogType(kind))
			return fetchErr
		})
	}
	if err != nil {
		_, _ = fmt.Fprintf(io.Err, "catalog unavailable (%v); serving %s without catalog flags\n", err, plan.Model)
		return fallback
	}
	entries = verify.FilterByEngine(entries, string(kind))
	for _, e := range entries {
		if e.Alias == plan.Model {
			return e
		}
	}
	return fallback
}

func applyModel(cfg *config.Config, opts Options, kind engine.Kind, platformURL string, io Streams) (verify.CatalogEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	defer cancel()

	var entries []verify.CatalogEntry
	var err error
	if opts.OfflineCatalog != "" {
		entries, err = compat.LoadOfflineCatalog(opts.OfflineCatalog, engine.CatalogType(kind))
	} else {
		msg := fmt.Sprintf("Fetching approved models from %s", platformURL)
		err = SpinWhile(io.Out, msg, func() error {
			var fetchErr error
			entries, fetchErr = compat.FetchCatalog(ctx, platformURL, engine.CatalogType(kind))
			return fetchErr
		})
		if err == nil {
			_, _ = fmt.Fprintf(io.Out, "Loaded %d models from %s\n", len(entries), platformURL)
		}
	}
	if err != nil {
		return verify.CatalogEntry{}, fmt.Errorf("catalog from %s: %w\nSet %s or --catalog-url to a reachable Flockadoo API, or pass --offline-catalog", platformURL, err, config.EnvPlatformURL)
	}

	before := len(entries)
	entries = verify.FilterByEngine(entries, string(kind))
	if before > 0 && len(entries) < before {
		_, _ = fmt.Fprintf(io.Out, "Filtered to %d models supported by %s\n", len(entries), engine.Label(kind))
	}

	hw, hwErr := compat.Detect(context.WithoutCancel(ctx)) // no time limit on detection, as before
	if hwErr != nil {
		_, _ = fmt.Fprintf(io.Err, "hardware detection warning: %v\n", hwErr)
	}
	results := compat.ScoreModels(hw, entries, kind)
	report := compat.BuildReport(hw, results, false)

	if opts.Model != "" {
		for _, e := range entries {
			if e.Alias == opts.Model {
				cfg.Provider.Model = e.Alias
				return e, nil
			}
		}
		return verify.CatalogEntry{}, fmt.Errorf("model %q is not supported by %s", opts.Model, engine.Label(kind))
	}
	if opts.Yes {
		if cfg.Provider.Model != "" {
			for _, e := range entries {
				if e.Alias == cfg.Provider.Model {
					return e, nil
				}
			}
		}
		return verify.CatalogEntry{}, fmt.Errorf("--model is required with --yes")
	}

	_, _ = fmt.Fprintln(io.Out, "\nModels that should fit this machine:")
	if err := compat.WriteTable(io.Out, report); err != nil {
		return verify.CatalogEntry{}, err
	}
	if len(report.Models) == 0 {
		return verify.CatalogEntry{}, fmt.Errorf("no fitting models in the catalog")
	}

	def := 1
	for i, m := range report.Models {
		if m.Alias == cfg.Provider.Model {
			def = i + 1
		}
	}
	choice, err := promptChoice(io.In, io.Out, len(report.Models), def)
	if err != nil {
		return verify.CatalogEntry{}, err
	}
	picked := report.Models[choice-1]
	var entry verify.CatalogEntry
	for _, e := range entries {
		if e.Alias == picked.Alias {
			entry = e
			break
		}
	}
	if entry.Alias == "" {
		entry.Alias = picked.Alias
		entry.HFRepo = picked.HFRepo
	}
	cfg.Provider.Model = entry.Alias
	return entry, nil
}

func applyServeOpts(cfg *config.Config, entry verify.CatalogEntry, hfRepo string) {
	opts := engine.ServeOptsFromCatalog(entry)
	cfg.Provider.ToolCallParser = opts.ToolCallParser
	cfg.Provider.ReasoningParser = opts.ReasoningParser
	cfg.Provider.MaxModelLen = opts.MaxModelLen
	cfg.Provider.RopeType = opts.RopeType
	cfg.Provider.RopeBaseContextLen = opts.RopeBaseContextLen
	cfg.Provider.RopeTheta = opts.RopeTheta
	cfg.Provider.Task = opts.Task
	if hfRepo != "" && hfRepo != entry.Alias {
		cfg.Provider.HFRepo = hfRepo
	} else {
		cfg.Provider.HFRepo = ""
	}
	if entry.GGUFFile != nil {
		cfg.Provider.GGUFFile = strings.TrimSpace(*entry.GGUFFile)
	} else {
		cfg.Provider.GGUFFile = ""
	}
}

func formatSpec(spec engine.Spec) string {
	return spec.Display()
}

const (
	docsFlockMaster = "https://docs.flockadoo.ai/provider/team-flocks#start-the-master-first"
	docsFlockWorker = "https://docs.flockadoo.ai/provider/joining-a-flock#start-when-the-owner-says"
)

func printFlockStart(out io.Writer, role string, spec engine.Spec) {
	_, _ = fmt.Fprintf(out, "\n%s\n", formatSpec(spec))
	if role == "worker" {
		_, _ = fmt.Fprintln(out, "Run this after the master is up. Then start flockadoo.")
		_, _ = fmt.Fprintln(out, docsFlockWorker)
		return
	}
	_, _ = fmt.Fprintln(out, "Start vLLM first using the command above. Workers join once the master is running and waiting. Then start flockadoo.")
	_, _ = fmt.Fprintln(out, docsFlockMaster)
}

// download, when set, fetches the pinned commit; it runs once the operator
// agrees to start, before the engine starts (model revisions C6).
func maybeStart(kind engine.Kind, spec engine.Spec, cfg *config.Config, opts Options, haveBin bool, io Streams, download func(context.Context) error) error {
	if opts.NoStart || !haveBin {
		_, _ = fmt.Fprintf(io.Out, "\nTo serve this model later:\n  %s\n", formatSpec(spec))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	defer cancel()
	if engine.Healthy(ctx, kind, cfg.Provider.LLMURL) {
		_, _ = fmt.Fprintf(io.Out, "\n%s is already running at %s.\n", kind, cfg.Provider.LLMURL)
		_, _ = fmt.Fprintf(io.Out, "To serve the chosen model, run:\n  %s\n", formatSpec(spec))
		if download != nil {
			_, _ = fmt.Fprintln(io.Out, "The model is not downloaded yet. Download it first with: flockadoo model download")
		}
		return nil
	}
	model := strings.TrimSpace(cfg.Provider.Model)
	if model == "" {
		model = "the model"
	}
	waitMsg := fmt.Sprintf("Waiting for %s to finish downloading and start serving at %s", model, cfg.Provider.LLMURL)

	if engine.PortOpen(ctx, cfg.Provider.LLMURL) {
		_, _ = fmt.Fprintf(io.Out, "\n%s is already starting at %s (port in use).\n", kind, cfg.Provider.LLMURL)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), engine.DefaultDownloadTimeout) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
		defer waitCancel()
		err := SpinWhile(io.Out, waitMsg, func() error {
			return engine.WaitHealthy(waitCtx, kind, cfg.Provider.LLMURL, 2*time.Second, nil)
		})
		if err != nil {
			return fmt.Errorf("%w (see %s)", err, engine.LogPath(cfg.Logging.LogDir))
		}
		_, _ = fmt.Fprintf(io.Out, "Engine is ready at %s.\n", cfg.Provider.LLMURL)
		return nil
	}

	_, _ = fmt.Fprintf(io.Out, "\n%s\n", formatSpec(spec))
	startNow := opts.Yes
	if !opts.Yes {
		ok, err := promptYes(io.In, io.Out, fmt.Sprintf("Start %s with this model now?", kind))
		if err != nil {
			return err
		}
		startNow = ok
	}
	if !startNow {
		if download != nil {
			_, _ = fmt.Fprintln(io.Out, "The model is not downloaded yet. Download it first with: flockadoo model download")
		}
		return nil
	}
	if download != nil {
		if err := download(context.Background()); err != nil { //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
			return err
		}
	}

	_, _ = fmt.Fprintf(io.Out, "Starting %s...\n", kind)
	logPath := engine.LogPath(cfg.Logging.LogDir)
	exited, err := startDetached(spec, logPath)
	if err != nil {
		return err
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), engine.DefaultDownloadTimeout) //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	defer waitCancel()
	if err := SpinWhile(io.Out, waitMsg, func() error {
		return engine.WaitHealthy(waitCtx, kind, cfg.Provider.LLMURL, 2*time.Second, exited)
	}); err != nil {
		return fmt.Errorf("%w (see %s)", err, logPath)
	}
	_, _ = fmt.Fprintf(io.Out, "Engine is ready at %s.\n", cfg.Provider.LLMURL)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
