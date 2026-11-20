package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Spec is the argv used to start an engine.
type Spec struct {
	Bin  string
	Args []string
	Env  []string
	URL  string
}

// ServeOpts are catalog-driven flags appended to vLLM / llama.cpp serve commands.
type ServeOpts struct {
	ToolCallParser string
	// ReasoningParser is vLLM's --reasoning-parser, so a reasoning model's
	// thinking is sent apart from its answer. llama.cpp splits thinking out
	// itself once --jinja is on.
	ReasoningParser    string
	MaxModelLen        int64
	RopeType           string
	RopeBaseContextLen int64
	RopeTheta          float64
	GGUFFile           string
	// Task is chat or embedding. llama.cpp needs --embeddings to serve /v1/embeddings.
	Task string
}

// ErrIncompleteRope marks RoPE settings vLLM cannot be given: a rope_type
// without the base context or the model's rope_theta.
var ErrIncompleteRope = errors.New("incomplete RoPE settings")

// Validate reports RoPE settings that cannot make a vLLM override: the override
// replaces the model's rope_parameters, so it needs all of them. llama.cpp
// ignores RoPE settings; callers check only vLLM and vLLM Metal.
func (o ServeOpts) Validate() error {
	if strings.TrimSpace(o.RopeType) == "" || o.ropeComplete() {
		return nil
	}
	return fmt.Errorf("%w: rope_type %s needs max_model_len, rope_base_context_len and the model's rope_theta (max_model_len %d, rope_base_context_len %d, rope_theta %v)",
		ErrIncompleteRope, strings.TrimSpace(o.RopeType), o.MaxModelLen, o.RopeBaseContextLen, o.RopeTheta)
}

func (o ServeOpts) ropeComplete() bool {
	return strings.TrimSpace(o.RopeType) != "" && o.MaxModelLen > 0 && o.RopeBaseContextLen > 0 && o.RopeTheta > 0
}

// ServeOptsFromConfig is the serve flags setup saved, for every command built
// from a saved config: auto-start, the flock refresh and the printed command.
func ServeOptsFromConfig(cfg *config.Config) ServeOpts {
	return ServeOpts{
		ToolCallParser:     cfg.Provider.ToolCallParser,
		ReasoningParser:    cfg.Provider.ReasoningParser,
		MaxModelLen:        cfg.Provider.MaxModelLen,
		RopeType:           cfg.Provider.RopeType,
		RopeBaseContextLen: cfg.Provider.RopeBaseContextLen,
		RopeTheta:          cfg.Provider.RopeTheta,
		GGUFFile:           cfg.Provider.GGUFFile,
		Task:               cfg.Provider.Task,
	}
}

// ServeOptsFromCatalog maps public catalog fields onto ServeOpts.
func ServeOptsFromCatalog(entry verify.CatalogEntry) ServeOpts {
	opts := ServeOpts{}
	if entry.ToolCallParser != nil {
		opts.ToolCallParser = strings.TrimSpace(*entry.ToolCallParser)
	}
	if entry.ReasoningParser != nil {
		opts.ReasoningParser = strings.TrimSpace(*entry.ReasoningParser)
	}
	if entry.MaxModelLen != nil {
		opts.MaxModelLen = *entry.MaxModelLen
	}
	if entry.RopeType != nil {
		opts.RopeType = strings.TrimSpace(*entry.RopeType)
	}
	if entry.RopeBaseContextLen != nil {
		opts.RopeBaseContextLen = *entry.RopeBaseContextLen
	}
	if entry.RopeTheta != nil {
		opts.RopeTheta = *entry.RopeTheta
	}
	if entry.GGUFFile != nil {
		opts.GGUFFile = strings.TrimSpace(*entry.GGUFFile)
	}
	opts.Task = strings.TrimSpace(entry.Task)
	return opts
}

// Display is CommandLine with Env prefixed, ready to paste into a shell.
func (s Spec) Display() string {
	if len(s.Env) == 0 {
		return s.CommandLine()
	}
	return strings.Join(s.Env, " ") + " " + s.CommandLine()
}

// CommandLine is a copy-pasteable shell form of Spec.
func (s Spec) CommandLine() string {
	parts := make([]string, 0, 1+len(s.Args))
	if s.Bin != "" {
		parts = append(parts, quote(s.Bin))
	}
	for _, a := range s.Args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsAny(s, " \t\"'") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// ServeSpec builds the start command for kind.
// modelAlias is the Flockadoo catalog alias; hfRepo is the HuggingFace id.
func ServeSpec(kind Kind, bin, modelAlias, hfRepo string, opts ServeOpts) Spec {
	if bin == "" {
		bin = defaultBinName(kind)
	}
	spec := Spec{Bin: bin, URL: DefaultURL(kind)}
	switch kind {
	case KindVLLM, KindVLLMMetal:
		repo := firstNonEmpty(hfRepo, modelAlias)
		spec.Args = []string{"serve", repo, "--host", "127.0.0.1", "--port", "8000"}
		if modelAlias != "" && modelAlias != repo {
			spec.Args = append(spec.Args, "--served-model-name", modelAlias)
		}
		spec.Args = append(spec.Args, vllmServeFlags(opts)...)
		// vLLM does not count YaRN when it checks --max-model-len against the
		// model's own context, so a YaRN row needs this. Without a complete
		// override it is left out, and vLLM refuses the long context itself.
		if opts.ropeComplete() && opts.MaxModelLen > opts.RopeBaseContextLen {
			spec.Env = append(spec.Env, "VLLM_ALLOW_LONG_MAX_MODEL_LEN=1")
		}
	case KindLlamaCpp:
		repo := firstNonEmpty(hfRepo, modelAlias)
		name := firstNonEmpty(modelAlias, repo)
		spec.Args = []string{
			"--hf-repo", repo,
			"--hf-file", opts.GGUFFile,
			"--alias", name,
			"--host", "127.0.0.1",
			"--port", "8081",
			"-ngl", "99",
			"--metrics",
		}
		if opts.MaxModelLen > 0 {
			spec.Args = append(spec.Args, "-c", strconv.FormatInt(opts.MaxModelLen, 10))
		}
		if strings.TrimSpace(opts.ToolCallParser) != "" || strings.TrimSpace(opts.ReasoningParser) != "" {
			spec.Args = append(spec.Args, "--jinja")
		}
		// vLLM detects embedding models itself; llama-server answers 501 on
		// /v1/embeddings unless started with --embeddings.
		if IsEmbedding(opts.Task) {
			spec.Args = append(spec.Args, "--embeddings")
		}
	}
	return spec
}

// IsEmbedding reports whether a catalog task is an embedding model.
func IsEmbedding(task string) bool {
	return strings.EqualFold(strings.TrimSpace(task), "embedding")
}

func vllmServeFlags(opts ServeOpts) []string {
	var args []string
	if opts.MaxModelLen > 0 {
		args = append(args, "--max-model-len", strconv.FormatInt(opts.MaxModelLen, 10))
	}
	// vLLM sets a dict override on the loaded config with setattr, which on
	// transformers 5 replaces the model's whole rope_parameters. So the override
	// is that complete dict, rope_theta included: a rope_scaling dict there
	// leaves rope_theta None and vLLM crashes at load. Needs vLLM 0.30.0 or
	// newer (MinVLLMVersion). An incomplete set sends no override (Validate).
	if opts.ropeComplete() {
		factor := float64(opts.MaxModelLen) / float64(opts.RopeBaseContextLen)
		payload := map[string]any{
			"rope_parameters": map[string]any{
				"rope_type":                        strings.TrimSpace(opts.RopeType),
				"factor":                           factor,
				"original_max_position_embeddings": opts.RopeBaseContextLen,
				"rope_theta":                       opts.RopeTheta,
			},
		}
		raw, err := json.Marshal(payload)
		if err == nil {
			args = append(args, "--hf-overrides", string(raw))
		}
	}
	if parser := strings.TrimSpace(opts.ToolCallParser); parser != "" {
		args = append(args, "--enable-auto-tool-choice", "--tool-call-parser", parser)
	}
	if parser := strings.TrimSpace(opts.ReasoningParser); parser != "" {
		args = append(args, "--reasoning-parser", parser)
	}
	return args
}

func defaultBinName(kind Kind) string {
	if kind == KindLlamaCpp {
		return "llama-server"
	}
	return "vllm"
}

// HFRepo returns the HuggingFace repo id from a catalog entry.
func HFRepo(entry verify.CatalogEntry) string {
	if entry.HFRepo != nil && strings.TrimSpace(*entry.HFRepo) != "" {
		return strings.TrimSpace(*entry.HFRepo)
	}
	return entry.Alias
}

// MasterJoinAddr is the torch store rank 0 binds.
// vLLM logs distributed_init_method and then blocks in init_process_group;
// /v1/models stays down until the other ranks connect. Empty for workers
// (--headless) and single-node serves.
func MasterJoinAddr(args []string) string {
	if hasArg(args, "--headless") || flagVal(args, "--node-rank") != "0" {
		return ""
	}
	n, err := strconv.Atoi(flagVal(args, "--nnodes"))
	if err != nil || n < 2 {
		return ""
	}
	host := flagVal(args, "--master-addr")
	port := flagVal(args, "--master-port")
	if host == "" || port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

// MasterURL is the master's HTTP API as seen from a headless worker.
// Empty for the master and single-node serves.
func MasterURL(args []string) string {
	if !Headless(args) {
		return ""
	}
	host := flagVal(args, "--master-addr")
	port := flagVal(args, "--port")
	if host == "" || port == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

// NNodes is the --nnodes value. Zero when this serve is not multi-node.
func NNodes(args []string) int {
	n, err := strconv.Atoi(flagVal(args, "--nnodes"))
	if err != nil || n < 2 {
		return 0
	}
	return n
}

// NodeSlots counts this machine plus online peers, out of --nnodes.
// self is true when this process has a Winglink address.
func NodeSlots(self bool, onlinePeers, nnodes int) (online, need int) {
	if nnodes < 2 {
		return 0, 0
	}
	need = nnodes
	online = onlinePeers
	if self {
		online++
	}
	if online > need {
		online = need
	}
	if online < 0 {
		online = 0
	}
	return online, need
}

// WorkerSlots is how many other nodes are online, out of how many the serve expects.
func WorkerSlots(onlinePeers, nnodes int) (joined, need int) {
	need = nnodes - 1
	if need < 1 {
		return 0, 0
	}
	joined = onlinePeers
	if joined > need {
		joined = need
	}
	if joined < 0 {
		joined = 0
	}
	return joined, need
}

func flagVal(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// Headless is a flock worker. It never opens an HTTP port.
func Headless(args []string) bool {
	return hasArg(args, "--headless")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
