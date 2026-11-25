package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Flockadoo/flockadoo/pkg/logger"
	"gopkg.in/yaml.v3"
)

// DefaultPlatformURL is the production Flockadoo API.
const DefaultPlatformURL = "https://core.flockadoo.ai"

// EnvPlatformURL is the testing override for the Flockadoo API base.
const EnvPlatformURL = "FLOCKADOO_URL"

// Config represents the application configuration
type Config struct {
	// Server configuration
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
		// MaxConcurrentInference is set only when the provider chooses a limit.
		// Nil lets the benchmark (or the engine default) decide. It must be at
		// least 1: there is no "unlimited" (decision DC4).
		MaxConcurrentInference *int `yaml:"max_concurrent_inference,omitempty"`
		// SessionQueueLimit is how many returning turns of conversations may
		// wait for a slot at once; beyond it a turn gets 503 at once and the
		// platform sends it elsewhere. Default 32, at least 1.
		SessionQueueLimit int `yaml:"session_queue_limit,omitempty"`
		// SessionQueueWaitSeconds bounds how long a same-session request
		// (matching X-Session-Key) waits for the inference slot instead of
		// being rejected with 503. Default 90.
		SessionQueueWaitSeconds int `yaml:"session_queue_wait_seconds"`
		// RequestTimeoutSeconds sets the HTTP server read/write timeouts.
		// Must cover the session queue wait plus inference so the
		// platform's non-streaming deadline is usable. Default and minimum
		// 420; a lower value is raised to it.
		RequestTimeoutSeconds int `yaml:"request_timeout_seconds"`
	} `yaml:"server"`

	// IgnoredHFHubCache is a provider.hf_hub_cache line left in an old file
	// (model revisions H1). The option is gone: it only ever reached the
	// verifier, never hf or the engine. It is read only so the client can say
	// it is ignored, and Save does not write it back. HF_HUB_CACHE moves the
	// Hugging Face cache.
	IgnoredHFHubCache string `yaml:"-"`

	// Provider configuration
	Provider struct {
		APIKey string `yaml:"api_key"`
		URL    string `yaml:"url"`
		// Engine is the local inference binary: vllm, vllm-metal, llama_cpp.
		// Empty means vllm. It is the one engine setting: the provider type
		// the server is told is derived from it (PlatformType).
		Engine string `yaml:"engine,omitempty"`
		// EngineBin is an absolute path when the binary is not on PATH.
		EngineBin string `yaml:"engine_bin,omitempty"`
		LLMURL    string `yaml:"llm_url"`
		// Model is the catalog alias (or HF repo) used to start the engine.
		Model string `yaml:"model,omitempty"`
		// AutoStart, when true, starts the engine if llm_url is down.
		AutoStart bool   `yaml:"auto_start"`
		ModelPath string `yaml:"model_path,omitempty"` // optional flat dir override (hf download --local-dir)
		// LLMTimeoutSeconds is the timeout for non-streaming requests
		// forwarded to the local vLLM, vLLM Metal, or llama.cpp instance.
		// Default and minimum 330; a lower value is raised to it.
		LLMTimeoutSeconds int `yaml:"llm_timeout_seconds"`
		// Catalog serve flags (SNTNL-119). Persisted from approved-builds so
		// auto-start can rebuild the same argv without a catalog fetch.
		ToolCallParser string `yaml:"tool_call_parser,omitempty"`
		// ReasoningParser is vLLM's --reasoning-parser for a model that
		// thinks before it answers; on llama.cpp it turns on --jinja.
		ReasoningParser    string  `yaml:"reasoning_parser,omitempty"`
		MaxModelLen        int64   `yaml:"max_model_len,omitempty"`
		RopeType           string  `yaml:"rope_type,omitempty"`
		RopeBaseContextLen int64   `yaml:"rope_base_context_len,omitempty"`
		RopeTheta          float64 `yaml:"rope_theta,omitempty"`
		// HFRepo is the HuggingFace id when it differs from Model (alias).
		HFRepo string `yaml:"hf_repo,omitempty"`
		// GGUFFile is the HuggingFace filename llama.cpp serves.
		GGUFFile string `yaml:"gguf_file,omitempty"`
		// HFRevision is the commit the engine serves (model revisions C4):
		// written when the client downloads or chooses a cached one.
		HFRevision string `yaml:"hf_revision,omitempty"`
		// Task is chat or embedding, from the catalog. Empty means chat.
		Task string `yaml:"task,omitempty"`
		// FlockRole is single, master, or worker. Empty means single.
		FlockRole string `yaml:"flock_role,omitempty"`
		// NodeToken is the worker (or master) credential for the flock's routes.
		NodeToken string `yaml:"node_token,omitempty"`
		// EngineArgs and EngineEnv are the last server-built serve command.
		EngineArgs []string `yaml:"engine_args,omitempty"`
		EngineEnv  []string `yaml:"engine_env,omitempty"`
	} `yaml:"provider"`

	// Logging configuration
	Logging logger.Config `yaml:"logging"`
}

// Defaults is the configuration before a file is read. Load starts from it,
// and so does the setup wizard when there is no file yet.
func Defaults() *Config {
	cfg := &Config{}
	cfg.Server.Port = 8080
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.SessionQueueWaitSeconds = 90
	cfg.Server.SessionQueueLimit = 32
	cfg.Server.RequestTimeoutSeconds = minRequestTimeoutSeconds
	cfg.Provider.URL = DefaultPlatformURL
	cfg.Provider.Engine = "vllm"
	cfg.Provider.LLMURL = "http://127.0.0.1:8000"
	cfg.Provider.LLMTimeoutSeconds = minLLMTimeoutSeconds
	if homeDir, err := os.UserHomeDir(); err == nil {
		cfg.Logging.LogDir = filepath.Join(homeDir, ".local", "state", "flockadoo", "log")
	}
	cfg.Logging.Level = "info"
	cfg.Logging.MaxSize = 100
	cfg.Logging.MaxBackups = 5
	cfg.Logging.MaxAge = 30
	return cfg
}

// Load loads the configuration from a YAML file. A provider_type line left
// in an old file is ignored: engine is the one setting (decision DC11).
func Load(path string) (*Config, error) {
	cfg := Defaults()

	// Read configuration file
	data, err := os.ReadFile(path)
	if err != nil {
		// If file doesn't exist, use default configuration
		if os.IsNotExist(err) {
			cfg.normalize()
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read configuration file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration file: %w", err)
	}
	var leftover struct {
		Provider struct {
			HFHubCache string `yaml:"hf_hub_cache"`
		} `yaml:"provider"`
	}
	if err := yaml.Unmarshal(data, &leftover); err == nil {
		cfg.IgnoredHFHubCache = strings.TrimSpace(leftover.Provider.HFHubCache)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	cfg.normalize()
	return cfg, nil
}

// validate refuses settings the client cannot run with.
func (c *Config) validate() error {
	if v := c.Server.MaxConcurrentInference; v != nil && *v < 1 {
		return fmt.Errorf("server.max_concurrent_inference is %d; set it to 1 or more, or remove it to let the client measure it (there is no unlimited)", *v)
	}
	if c.Server.SessionQueueLimit < 1 {
		return fmt.Errorf("server.session_queue_limit is %d; set it to 1 or more, or remove it for the default of 32", c.Server.SessionQueueLimit)
	}
	return nil
}

func (c *Config) normalize() {
	c.Provider.Engine = strings.ToLower(strings.TrimSpace(c.Provider.Engine))
	c.Provider.EngineBin = strings.TrimSpace(c.Provider.EngineBin)
	c.Provider.Model = strings.TrimSpace(c.Provider.Model)
	c.Provider.ToolCallParser = strings.TrimSpace(c.Provider.ToolCallParser)
	c.Provider.ReasoningParser = strings.TrimSpace(c.Provider.ReasoningParser)
	c.Provider.RopeType = strings.TrimSpace(c.Provider.RopeType)
	c.Provider.HFRepo = strings.TrimSpace(c.Provider.HFRepo)
	c.Provider.GGUFFile = strings.TrimSpace(c.Provider.GGUFFile)
	c.Provider.Task = strings.ToLower(strings.TrimSpace(c.Provider.Task))
	if c.Provider.Engine == "" {
		c.Provider.Engine = "vllm"
	}
}

// PlatformType is the provider type the server is told for this config's
// engine: llama_cpp, or vllm for vLLM and vLLM Metal.
func (c *Config) PlatformType() string {
	return PlatformType(c.Provider.Engine)
}

// ExplicitConcurrency reports a limit the provider wrote into config.
// The second result is false when the benchmark or engine default should decide.
func (c *Config) ExplicitConcurrency() (int, bool) {
	if c == nil || c.Server.MaxConcurrentInference == nil {
		return 0, false
	}
	return *c.Server.MaxConcurrentInference, true
}

// PlatformType is the Flockadoo provider_type for a local engine.
func PlatformType(engine string) string {
	if strings.EqualFold(strings.TrimSpace(engine), "llama_cpp") {
		return "llama_cpp"
	}
	return "vllm"
}

// DefaultPath is ~/.config/flockadoo/config.yaml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "flockadoo", "config.yaml"), nil
}

// HasAPIKey reports whether api_key is set to a real value.
func (c *Config) HasAPIKey() bool {
	k := strings.TrimSpace(c.Provider.APIKey)
	return k != "" && k != "your_api_key_here"
}

// EnrolledWorker is a machine that already traded a join code for a node token.
func (c *Config) EnrolledWorker() bool {
	if c == nil {
		return false
	}
	return c.Provider.FlockRole == "worker" && strings.TrimSpace(c.Provider.NodeToken) != ""
}

// Save writes cfg to path, creating the parent directory.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal configuration: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write configuration file: %w", err)
	}
	return nil
}

// SessionQueueWait is the bounded wait for same-session queued requests.
func (c *Config) SessionQueueWait() time.Duration {
	return time.Duration(c.Server.SessionQueueWaitSeconds) * time.Second
}

const (
	minLLMTimeoutSeconds     = 330
	minRequestTimeoutSeconds = 420
)

// RequestTimeout is the HTTP server read/write timeout. Values below the
// platform non-streaming deadline are raised so a valid request is not cut
// into a fallback.
func (c *Config) RequestTimeout() time.Duration {
	secs := c.Server.RequestTimeoutSeconds
	if secs < minRequestTimeoutSeconds {
		secs = minRequestTimeoutSeconds
	}
	return time.Duration(secs) * time.Second
}

// LLMTimeout is the timeout for non-streaming requests forwarded to the local LLM.
func (c *Config) LLMTimeout() time.Duration {
	secs := c.Provider.LLMTimeoutSeconds
	if secs < minLLMTimeoutSeconds {
		secs = minLLMTimeoutSeconds
	}
	return time.Duration(secs) * time.Second
}

// LocalDashboardURL is the loopback URL for the in-process status page.
func (c *Config) LocalDashboardURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", c.Server.Port)
}

// PlatformURLFromEnv returns FLOCKADOO_URL when set.
func PlatformURLFromEnv() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(EnvPlatformURL)), "/")
}

// APIKeyFromEnv returns PROVIDER_API_KEY: the setup wizard's default key.
func APIKeyFromEnv() string {
	return os.Getenv("PROVIDER_API_KEY")
}

// EngineFromEnv returns PROVIDER_TYPE, trimmed: setup's default engine.
func EngineFromEnv() string {
	return strings.TrimSpace(os.Getenv("PROVIDER_TYPE"))
}

// InDocker reports whether the client runs in its Docker image, which sets
// FLOCKADOO_IN_DOCKER=1. The image is updated by pulling, not by self-update.
func InDocker() bool {
	return os.Getenv("FLOCKADOO_IN_DOCKER") == "1"
}

// ResolvePlatformURL picks the Flockadoo API base.
// Order: explicit flag, FLOCKADOO_URL, existing config (as written), production default.
func ResolvePlatformURL(explicit, fromConfig string) string {
	if v := strings.TrimRight(strings.TrimSpace(explicit), "/"); v != "" {
		return v
	}
	if v := PlatformURLFromEnv(); v != "" {
		return v
	}
	if v := strings.TrimRight(strings.TrimSpace(fromConfig), "/"); v != "" {
		return v
	}
	return DefaultPlatformURL
}

// ApplyEnvOverrides applies process env on top of a loaded config.
func ApplyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if u := PlatformURLFromEnv(); u != "" {
		cfg.Provider.URL = u
	}
}
