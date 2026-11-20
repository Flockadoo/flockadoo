package setup

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
)

// credential keeps a join code pasted with a label or quotes.
func credential(s string) string {
	s = strings.TrimSpace(s)
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "`\"'.,:;")
		if strings.HasPrefix(f, "join-") {
			return f
		}
	}
	return strings.Trim(s, "`\"'")
}

func onlinePeers(ctx context.Context) int {
	_, n := winglink.Status(ctx)
	return n
}

// flockSettings is what setup shows instead of the single-machine model list.
type flockSettings struct {
	Role        string
	Name        string
	Model       string
	DisplayName string
	ServiceType string
	VLLMVersion string
	MasterGPU   string
}

// flockPlan is replaced in tests. Production reads the flock the key belongs to.
var flockPlan = flockPlanReal

func flockPlanReal(cfg *config.Config) (flockSettings, error) {
	ctx := context.Background() //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	if cfg.EnrolledWorker() {
		model := strings.TrimSpace(cfg.Provider.Model)
		if model == "" {
			return flockSettings{}, fmt.Errorf("this machine is in a flock but has no saved model. Re-run setup with a new join code")
		}
		settings := flockSettings{
			Role: "worker", Model: model, ServiceType: firstNonEmpty(cfg.Provider.Engine, "vllm"),
		}
		remote, err := winglink.FetchEngine(ctx, cfg.Provider.URL, cfg.Provider.NodeToken, true)
		if err == nil {
			settings.VLLMVersion = remote.VLLMVersion
			settings.MasterGPU = remote.MasterGPU
		}
		return settings, nil
	}
	key := credential(cfg.Provider.APIKey)
	if strings.HasPrefix(key, "join-") {
		info, err := winglink.PreviewJoin(ctx, cfg.Provider.URL, key)
		if err != nil {
			return flockSettings{}, fmt.Errorf("join code: %w", err)
		}
		if strings.TrimSpace(info.Model) == "" {
			return flockSettings{}, fmt.Errorf("flock %s has no model set. Choose one in the dashboard, then re-run setup", firstNonEmpty(info.Name, "this flock"))
		}
		return flockSettings{
			Role: "worker", Name: info.Name, Model: info.Model,
			DisplayName: info.ModelDisplayName, ServiceType: info.ServiceType,
			VLLMVersion: info.VLLMVersion, MasterGPU: info.MasterGPU,
		}, nil
	}
	sess, err := winglink.FetchSession(ctx, cfg.Provider.URL, key)
	if err != nil || sess.Mode != "multi" {
		return flockSettings{Role: "single"}, err
	}
	if strings.TrimSpace(sess.Model) == "" {
		return flockSettings{}, fmt.Errorf("flock %s has no model set. Choose one in the dashboard, then re-run setup", firstNonEmpty(sess.Name, "this flock"))
	}
	return flockSettings{
		Role: "master", Name: sess.Name, Model: sess.Model,
		DisplayName: sess.ModelDisplayName, ServiceType: sess.ServiceType,
		VLLMVersion: sess.VLLMVersion, MasterGPU: sess.MasterGPU,
	}, nil
}

// prepareFlock is replaced in tests. Production joins Winglink when the
// key is a multi-node master key or a join- code, then rewrites the engine spec.
var prepareFlock = prepareFlockReal

func decideFlock(key, mode string, lookupErr error) string {
	if strings.HasPrefix(credential(key), "join-") {
		return "worker"
	}
	if lookupErr != nil || mode != "multi" {
		return "single"
	}
	return "master"
}

// NodeRemoved is the server API rejecting a token after the owner removed the machine.
func NodeRemoved(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid node token")
}

func removedErr(err error) error {
	return fmt.Errorf("the owner removed this machine from the flock. Re-run setup and paste a new join code: %w", err)
}

func dropIfRemoved(cfg *config.Config, err error) error {
	if !NodeRemoved(err) {
		return err
	}
	cfg.Provider.NodeToken = ""
	cfg.Provider.FlockRole = ""
	return removedErr(err)
}

func resumeWorker(ctx context.Context, cfg *config.Config, spec *engine.Spec) error {
	token := cfg.Provider.NodeToken
	remote, err := winglink.FetchEngine(ctx, cfg.Provider.URL, token, true)
	if err != nil {
		return dropIfRemoved(cfg, err)
	}
	ip, nodeID, err := winglink.BringUp(ctx, remote.LoginServer, "")
	if err != nil {
		return err
	}
	if err := winglink.ReportIP(ctx, cfg.Provider.URL, token, true, ip, nodeID, localGPU(ctx), engine.InstalledVersion(ctx, spec.Bin)); err != nil {
		return dropIfRemoved(cfg, err)
	}
	next := winglink.Apply(*spec, remote)
	*spec = next
	cfg.Provider.EngineArgs = next.Args
	cfg.Provider.EngineEnv = next.Env
	cfg.Provider.LLMURL = next.URL
	cfg.Provider.FlockRole = "worker"
	return nil
}

func prepareFlockReal(cfg *config.Config, spec *engine.Spec) error {
	ctx := context.Background() //nolint:forbidigo // the wizard has no caller context yet (CLU-4, item K1)
	key := credential(cfg.Provider.APIKey)
	cfg.Provider.APIKey = key
	if cfg.EnrolledWorker() {
		return resumeWorker(ctx, cfg, spec)
	}
	worker := strings.HasPrefix(key, "join-")
	var login, auth, token, role string
	if worker {
		res, err := winglink.Exchange(ctx, cfg.Provider.URL, key)
		if err != nil {
			return err
		}
		login, auth, token = res.LoginServer, res.AuthKey, res.NodeToken
		role = "worker"
	} else {
		sess, err := winglink.FetchSession(ctx, cfg.Provider.URL, key)
		role = decideFlock(key, sess.Mode, err)
		if role == "single" {
			if err != nil {
				// A failed lookup must not wipe a saved flock command.
				fmt.Fprintf(os.Stderr, "could not read the flock's mode (%v); continuing as a Solo Flock\n", err)
				return nil
			}
			cfg.Provider.FlockRole = ""
			cfg.Provider.NodeToken = ""
			cfg.Provider.EngineArgs = append([]string(nil), spec.Args...)
			cfg.Provider.EngineEnv = append([]string(nil), spec.Env...)
			return nil
		}
		login = sess.LoginServer
		if cfg.Provider.NodeToken == "" {
			res, err := winglink.Enroll(ctx, cfg.Provider.URL, key)
			if err != nil {
				return err
			}
			login, auth, token = res.LoginServer, res.AuthKey, res.NodeToken
		}
		token = firstNonEmpty(token, cfg.Provider.NodeToken)
	}
	cfg.Provider.FlockRole = role
	if token != "" {
		cfg.Provider.NodeToken = token
	}
	ip, nodeID, err := winglink.BringUp(ctx, login, auth)
	if err != nil && !worker && auth == "" {
		// The previous attempt saved a node token before winglink up ran.
		res, enrollErr := winglink.Enroll(ctx, cfg.Provider.URL, key)
		if enrollErr != nil {
			return err
		}
		login, auth, token = res.LoginServer, res.AuthKey, res.NodeToken
		if token != "" {
			cfg.Provider.NodeToken = token
		}
		ip, nodeID, err = winglink.BringUp(ctx, login, auth)
	}
	if err != nil {
		return err
	}
	bearer := key
	if worker {
		bearer = cfg.Provider.NodeToken
	}
	if err := winglink.ReportIP(ctx, cfg.Provider.URL, bearer, worker, ip, nodeID, localGPU(ctx), engine.InstalledVersion(ctx, spec.Bin)); err != nil {
		return err
	}
	remote, err := winglink.FetchEngine(ctx, cfg.Provider.URL, bearer, worker)
	if err != nil {
		return err
	}
	next := winglink.Apply(*spec, remote)
	*spec = next
	cfg.Provider.EngineArgs = next.Args
	cfg.Provider.EngineEnv = next.Env
	cfg.Provider.LLMURL = next.URL
	return nil
}

// loginServer is the URL the daemon dials. The engine route has it once a
// Winglink address is stored; otherwise the master session does.
func loginServer(ctx context.Context, cfg *config.Config, worker bool) string {
	bearer := cfg.Provider.APIKey
	if worker {
		bearer = cfg.Provider.NodeToken
	}
	if remote, err := winglink.FetchEngine(ctx, cfg.Provider.URL, bearer, worker); err == nil && remote.LoginServer != "" {
		return remote.LoginServer
	}
	if worker {
		return ""
	}
	sess, err := winglink.FetchSession(ctx, cfg.Provider.URL, cfg.Provider.APIKey)
	if err != nil {
		return ""
	}
	return sess.LoginServer
}

// refreshFlock is replaced in tests.
var refreshFlock = refreshFlockEngine

// refreshFlockEngine reloads the server command before a later start.
// It also reconnects winglink when the saved address is offline.
func refreshFlockEngine(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	mode := cfg.Provider.FlockRole
	if mode != "multi" && mode != "worker" && mode != "master" {
		return nil
	}
	worker := mode == "worker"
	bearer := cfg.Provider.APIKey
	if worker {
		bearer = cfg.Provider.NodeToken
		if _, err := winglink.FetchEngine(ctx, cfg.Provider.URL, bearer, true); NodeRemoved(err) {
			return removedErr(err)
		}
	}
	ip, nodeID, err := winglink.BringUp(ctx, loginServer(ctx, cfg, worker), "")
	if err != nil {
		return err
	}
	if err := winglink.ReportIP(ctx, cfg.Provider.URL, bearer, worker, ip, nodeID, localGPU(ctx), engine.InstalledVersion(ctx, cfg.Provider.EngineBin)); err != nil {
		if NodeRemoved(err) {
			return removedErr(err)
		}
		return err
	}
	remote, err := winglink.FetchEngine(ctx, cfg.Provider.URL, bearer, worker)
	if err != nil {
		if NodeRemoved(err) {
			return removedErr(err)
		}
		return err
	}
	kind, ok := engine.ParseKind(cfg.Provider.Engine)
	if !ok {
		return fmt.Errorf("unknown engine %q", cfg.Provider.Engine)
	}
	serveOpts := engine.ServeOptsFromConfig(cfg)
	if kind != engine.KindLlamaCpp {
		if err := serveOpts.Validate(); err != nil {
			return fmt.Errorf("%w. Run flockadoo setup again to save the model's current settings", err)
		}
	}
	spec := engine.ServeSpec(kind, engine.BinPath(kind, cfg.Provider.EngineBin), cfg.Provider.Model, firstNonEmpty(cfg.Provider.HFRepo, cfg.Provider.Model), serveOpts)
	next := winglink.Apply(spec, remote)
	cfg.Provider.EngineArgs = next.Args
	cfg.Provider.EngineEnv = next.Env
	cfg.Provider.LLMURL = next.URL
	return nil
}
