package verify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"go.uber.org/zap"
)

type fileStat struct {
	size    int64
	modTime int64
}

type fingerprintCache struct {
	files []FileMeasurement
	stats map[string]fileStat
}

// Verifier measures local models and asks the platform to judge verification status.
type Verifier struct {
	catalog           *Catalog
	server            *ServerClient
	serviceType       string
	hfHubCache        string
	modelPathOverride string

	mu        sync.Mutex
	cache     map[string]*fingerprintCache // alias -> weight fingerprint cache (vLLM)
	resultTTL time.Duration                // RefreshAfter
	grace     time.Duration
	states    map[string]*modelState
}

// SetEngineRefusal is a stub with today's behaviour: it does nothing.
func (v *Verifier) SetEngineRefusal(reason string) {}

// Verdict is what the relay may do for a model now (plan item B3).
type Verdict int

const (
	Serve       Verdict = iota // verified: fresh, or inside the outage grace
	Refuse                     // the node answered, and not with "verified": 403
	Unavailable                // no usable answer and the node cannot be asked: 503
)

// errCatalogChanged marks every model due for a refresh.
var errCatalogChanged = errors.New("the approved catalog changed")

// Default timings (decision DC2): an answer is refreshed after 10 minutes,
// and a verified one may still be used for 30 minutes more while the node
// cannot be asked.
const (
	DefaultRefreshAfter = 10 * time.Minute
	DefaultOutageGrace  = 30 * time.Minute
)

// modelState is the last answer the node gave for one model and the key it
// belongs to. Only an answer from the node changes status and answered; a
// failure to ask sets lastErr and nothing else (invariant I14).
type modelState struct {
	entryID    string
	stats      map[string]fileStat
	status     Status
	answered   time.Time
	result     Result
	lastErr    error
	refreshing bool
}

// SetTiming sets how long an answer is fresh and, past that, how long a
// verified answer may still be used while the node cannot be asked.
func (v *Verifier) SetTiming(refreshAfter, outageGrace time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.resultTTL, v.grace = refreshAfter, outageGrace
}

// currentKey is the catalog entry and the weight files a verdict for model
// would belong to now.
func (v *Verifier) currentKey(model string) (entryID, root string, stats map[string]fileStat, err error) {
	entry, ok := v.catalog.Get(model)
	if !ok {
		return "", "", nil, fmt.Errorf("model %s is not in the approved catalog", model)
	}
	if v.serviceType == "llama_cpp" {
		root, err = v.resolveLlamaRoot(entry)
	} else {
		root, err = v.resolveVLLMRoot(entry, model)
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("locate weights for %s: %w", model, err)
	}
	stats, err = v.weightStats(model, root)
	return entry.ID, root, stats, err
}

// Verdict is the whole policy, read from state with no call to the node
// (decision DC2):
//
//	no answer for this model's current key         -> Unavailable (503)
//	the last answer was not "verified"             -> Refuse (403)
//	answered less than RefreshAfter+Grace ago      -> Serve
//	otherwise                                      -> Unavailable (503)
//
// A model outside the catalog, or whose weights are not on disk, is refused.
func (v *Verifier) Verdict(model string) (Verdict, string) {
	entryID, _, stats, err := v.currentKey(model)
	if err != nil {
		return Refuse, err.Error()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.states[model]
	if st == nil || st.answered.IsZero() || st.entryID != entryID || !fileStatsEqual(st.stats, stats) {
		return Unavailable, "model " + model + " is not verified yet"
	}
	if st.status != StatusVerified {
		return Refuse, string(st.status)
	}
	if time.Since(st.answered) < v.resultTTL+v.grace {
		return Serve, ""
	}
	return Unavailable, "model verification is temporarily unavailable"
}

// Status is what the health report says about model: the node's last
// answer, or nothing once a verified answer's grace has run out (plan item
// B3, detail 5: an outage is never reported as a failure).
func (v *Verifier) Status(model string) Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.states[model]
	if st == nil || st.answered.IsZero() {
		return ""
	}
	if st.status == StatusVerified && time.Since(st.answered) >= v.resultTTL+v.grace {
		return ""
	}
	return st.status
}

// due reports whether model's answer should be refreshed now.
func (v *Verifier) due(model string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.states[model]
	return st == nil || st.answered.IsZero() || st.lastErr != nil || time.Since(st.answered) >= v.resultTTL
}

// Refresh asks the node about model once. Hashing and the call run outside
// the lock a request takes, and two refreshes of one model never overlap.
// An answer replaces the state; a failure to ask records why and changes
// nothing else.
func (v *Verifier) Refresh(ctx context.Context, model string) {
	v.mu.Lock()
	st := v.states[model]
	if st == nil {
		st = &modelState{}
		v.states[model] = st
	}
	if st.refreshing {
		v.mu.Unlock()
		return
	}
	st.refreshing = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		st.refreshing = false
		v.mu.Unlock()
	}()

	entryID, root, stats, err := v.currentKey(model)
	if err != nil {
		v.answer(st, entryID, stats, Result{Alias: model, Status: StatusUnverified})
		return
	}
	files, stale, err := v.measureWithCache(model, root)
	if err != nil {
		v.answer(st, entryID, stats, Result{Alias: model, Status: StatusFailed})
		return
	}
	resp, err := v.server.VerifyModel(ctx, verifyModelRequest{Alias: model, ServiceType: v.serviceType, Files: files, Stale: stale})
	if err != nil {
		v.mu.Lock()
		if st.entryID != entryID || !fileStatsEqual(st.stats, stats) {
			// A new key inherits nothing (decision DC2).
			*st = modelState{entryID: entryID, stats: stats, refreshing: true}
		}
		st.lastErr = err
		v.mu.Unlock()
		if unavailable(err) {
			logger.Warn("model verification unavailable; serving on the last answer while the grace lasts", zap.String("model", model), zap.Error(err))
			return
		}
		v.answer(st, entryID, stats, Result{Alias: model, Status: StatusUnverified})
		return
	}
	res := Result{Alias: model}
	applyServerResponse(&res, resp)
	v.answer(st, entryID, stats, res)
}

func (v *Verifier) answer(st *modelState, entryID string, stats map[string]fileStat, res Result) {
	v.mu.Lock()
	defer v.mu.Unlock()
	st.entryID, st.stats = entryID, stats
	st.status, st.result, st.answered, st.lastErr = res.Status, res, time.Now(), nil
}

// RunRefresher refreshes the given models whenever they are due, checking
// every interval, until ctx ends. It is the only caller of verify-model on
// a running client besides the health report's first look.
func (v *Verifier) RunRefresher(ctx context.Context, interval time.Duration, models func() []string) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		for _, m := range models() {
			if m != "" && v.due(m) {
				v.Refresh(ctx, m)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// NewVerifier creates a verifier. Measurements are sent to the server; expected hashes stay in the DB.
func NewVerifier(catalog *Catalog, server *ServerClient, serviceType, hfHubCache, modelPathOverride string) *Verifier {
	return &Verifier{
		catalog:           catalog,
		server:            server,
		serviceType:       strings.ToLower(serviceType),
		hfHubCache:        strings.TrimSpace(hfHubCache),
		modelPathOverride: strings.TrimSpace(modelPathOverride),
		cache:             make(map[string]*fingerprintCache),
		resultTTL:         DefaultRefreshAfter,
		grace:             DefaultOutageGrace,
		states:            make(map[string]*modelState),
	}
}

func (v *Verifier) resolveVLLMRoot(entry CatalogEntry, alias string) (string, error) {
	if v.modelPathOverride != "" {
		abs, err := filepath.Abs(v.modelPathOverride)
		if err != nil {
			return "", err
		}
		if dirHasWeights(abs) {
			return abs, nil
		}
	}

	repo := hfRepoForCatalog(alias, entry)
	ref := hfRefForCatalog(entry)
	hub := v.hfHubCache
	if hub == "" {
		var err error
		hub, err = DefaultHFHubCache()
		if err != nil {
			return "", err
		}
	}
	return ResolveHFModelRoot(hub, repo, ref)
}

func (v *Verifier) measureWithCache(alias, root string) (files []FileMeasurement, stale bool, err error) {
	currentStats, err := v.weightStats(alias, root)
	if err != nil {
		return nil, false, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	prev, hadCache := v.cache[alias]
	if hadCache && fileStatsEqual(prev.stats, currentStats) {
		return prev.files, false, nil
	}

	if v.serviceType == "llama_cpp" {
		entry, _ := v.catalog.Get(alias)
		files, err = measureGGUF(root, hfRepoForCatalog(alias, entry), ggufName(entry))
	} else {
		files, err = measureWeightDir(root)
	}
	if err != nil {
		return nil, false, err
	}

	v.cache[alias] = &fingerprintCache{files: files, stats: currentStats}
	return files, hadCache, nil
}

func (v *Verifier) resolveLlamaRoot(entry CatalogEntry) (string, error) {
	if v.modelPathOverride != "" {
		return filepath.Abs(v.modelPathOverride)
	}
	repo := hfRepoForCatalog("", entry)
	if repo != "" {
		if snap, err := resolveGGUFSnapshot(repo, hfRefForCatalog(entry)); err == nil {
			return snap, nil
		}
	}
	return DefaultLlamaCacheDir()
}

func (v *Verifier) weightStats(alias, root string) (map[string]fileStat, error) {
	if v.serviceType != "llama_cpp" {
		return weightDirStats(root)
	}
	entry, _ := v.catalog.Get(alias)
	return ggufStats(root, hfRepoForCatalog(alias, entry), ggufName(entry))
}

func fileStatsEqual(a, b map[string]fileStat) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}

func isKnownStatus(s string) bool {
	switch Status(strings.TrimSpace(s)) {
	case StatusVerified, StatusPending, StatusStale, StatusFailed, StatusUnverified:
		return true
	default:
		return false
	}
}

func applyServerResponse(res *Result, resp verifyModelResponse) {
	if !isKnownStatus(resp.VerificationStatus) {
		res.Status = StatusFailed
		return
	}
	res.Status = Status(strings.TrimSpace(resp.VerificationStatus))
	if resp.WeightFingerprint != "" {
		res.WeightFingerprint = strings.ToLower(strings.TrimSpace(resp.WeightFingerprint))
	}
}

// ApplyToModels enriches discovered models with verification fields for the
// health report. It reads the state, refreshing a model that is due first;
// an outage leaves the last answer in place and is never reported as a
// failure (invariant I14).
func (v *Verifier) ApplyToModels(ctx context.Context, llmClient llm.Client, models []llm.Model) []llm.Model {
	out := make([]llm.Model, len(models))
	for i, m := range models {
		out[i] = m
		switch v.serviceType {
		case "vllm", "llama_cpp":
			if v.due(m.ID) {
				v.Refresh(ctx, m.ID)
			}
			v.mu.Lock()
			var res Result
			if st := v.states[m.ID]; st != nil {
				res = st.result
			}
			v.mu.Unlock()
			res.Status = v.Status(m.ID)
			applyResult(&out[i], res)
			if entry, ok := v.catalog.Get(m.ID); ok && res.Status == StatusVerified {
				applyContextGate(ctx, llmClient, &out[i], entry)
			}
		default:
			out[i].VerificationStatus = string(StatusUnverified)
		}
	}
	return out
}

func applyResult(m *llm.Model, res Result) {
	m.VerificationStatus = string(res.Status)
	m.WeightFingerprint = res.WeightFingerprint
	m.SizeBytes = res.SizeBytes
}

// IsInferenceAllowed returns true when the model may serve traffic.
func IsInferenceAllowed(status string) bool {
	return status == string(StatusVerified)
}

// CatalogClient exposes the public model catalog.
func (v *Verifier) CatalogClient() *Catalog {
	return v.catalog
}

// RefreshCatalog reloads the public approved-model catalog and clears verify cache when it changes.
func (v *Verifier) RefreshCatalog(ctx context.Context) error {
	if v.catalog == nil {
		return nil
	}
	before := v.catalog.fingerprint()
	if err := v.catalog.Refresh(ctx); err != nil {
		return err
	}
	if v.catalog.fingerprint() != before {
		// A changed catalog is checked again at the next refresh; the
		// verdicts stand until then (a changed entry id is a new key).
		v.mu.Lock()
		for _, st := range v.states {
			st.lastErr = errCatalogChanged
		}
		v.mu.Unlock()
	}
	return nil
}
