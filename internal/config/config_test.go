package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultHost(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("Load(%q) err = %v, want nil", missing, err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("default Server.Host = %q, want %q", cfg.Server.Host, "127.0.0.1")
	}
}

func TestLoadHostFromYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "omitted host keeps default",
			yaml: "server:\n  port: 9090\n",
			want: "127.0.0.1",
		},
		{
			name: "explicit all-interfaces is kept",
			yaml: "server:\n  host: \"0.0.0.0\"\n",
			want: "0.0.0.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load(%q) err = %v, want nil", path, err)
			}
			if cfg.Server.Host != tt.want {
				t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, tt.want)
			}
		})
	}
}

// Rewritten for decision DC11 (plan item Q2): the engine, and the provider
// type derived from it, come from engine alone; provider_type is not a
// setting any more, and a line of it changes nothing.
func TestLoadTakesTheEngineFromEngine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		yaml       string
		wantEngine string
		wantType   string
		wantAuto   bool
	}{
		{
			name:       "no engine defaults to vllm",
			yaml:       "provider:\n  model: Qwen/Qwen2.5-7B-Instruct\n",
			wantEngine: "vllm",
			wantType:   "vllm",
		},
		{
			name:       "a provider_type line alone changes nothing",
			yaml:       "provider:\n  provider_type: llama_cpp\n",
			wantEngine: "vllm",
			wantType:   "vllm",
		},
		{
			name:       "vllm-metal is a vllm provider",
			yaml:       "provider:\n  engine: vllm-metal\n",
			wantEngine: "vllm-metal",
			wantType:   "vllm",
		},
		{
			name:       "llama_cpp keeps its platform type",
			yaml:       "provider:\n  engine: llama_cpp\n  auto_start: true\n  model: Qwen/Qwen2.5-7B-Instruct\n",
			wantEngine: "llama_cpp",
			wantType:   "llama_cpp",
			wantAuto:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Provider.Engine != tt.wantEngine {
				t.Errorf("Engine = %q, want %q", cfg.Provider.Engine, tt.wantEngine)
			}
			if cfg.PlatformType() != tt.wantType {
				t.Errorf("PlatformType() = %q, want %q", cfg.PlatformType(), tt.wantType)
			}
			if cfg.Provider.AutoStart != tt.wantAuto {
				t.Errorf("AutoStart = %v, want %v", cfg.Provider.AutoStart, tt.wantAuto)
			}
		})
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	in := &Config{}
	in.Server.Port = 8080
	in.Server.Host = "127.0.0.1"
	in.Provider.APIKey = "test-key"
	in.Provider.URL = "https://core.inferoute.com"
	in.Provider.Engine = "llama_cpp"
	in.Provider.LLMURL = "http://127.0.0.1:8081"
	in.Provider.Model = "Qwen/Qwen2.5-7B-Instruct"
	in.Provider.AutoStart = true
	in.Provider.EngineBin = `C:\Users\you\AppData\Local\inferoute\llama.cpp\llama-server.exe`
	in.Provider.GGUFFile = "qwen2.5-7b-instruct-q4_k_m.gguf"
	in.Provider.ToolCallParser = "hermes"
	in.Provider.ReasoningParser = "qwen3"
	in.Provider.MaxModelLen = 131072
	in.Provider.RopeType = "yarn"
	in.Provider.RopeBaseContextLen = 32768

	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Provider.Engine != "llama_cpp" || out.PlatformType() != "llama_cpp" {
		t.Errorf("engine/type = %q/%q", out.Provider.Engine, out.PlatformType())
	}
	if !out.Provider.AutoStart || out.Provider.Model != "Qwen/Qwen2.5-7B-Instruct" {
		t.Errorf("auto_start/model = %v/%q", out.Provider.AutoStart, out.Provider.Model)
	}
	if out.Provider.EngineBin != in.Provider.EngineBin {
		t.Errorf("EngineBin = %q, want %q", out.Provider.EngineBin, in.Provider.EngineBin)
	}
	if out.Provider.ToolCallParser != "hermes" || out.Provider.ReasoningParser != "qwen3" || out.Provider.MaxModelLen != 131072 ||
		out.Provider.RopeType != "yarn" || out.Provider.RopeBaseContextLen != 32768 {
		t.Errorf("serve flags = %+v", out.Provider)
	}
	if out.Provider.GGUFFile != "qwen2.5-7b-instruct-q4_k_m.gguf" {
		t.Errorf("GGUFFile = %q", out.Provider.GGUFFile)
	}
}

func TestHasAPIKey(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	if cfg.HasAPIKey() {
		t.Fatal("empty key should be false")
	}
	cfg.Provider.APIKey = "your_api_key_here"
	if cfg.HasAPIKey() {
		t.Fatal("placeholder should be false")
	}
	cfg.Provider.APIKey = "real"
	if !cfg.HasAPIKey() {
		t.Fatal("real key should be true")
	}
}

func TestTimeoutFloors(t *testing.T) {
	cfg := &Config{}
	cfg.Provider.LLMTimeoutSeconds = 120
	cfg.Server.RequestTimeoutSeconds = 240
	if cfg.LLMTimeout() < 330*time.Second {
		t.Fatalf("llm timeout %s", cfg.LLMTimeout())
	}
	if cfg.RequestTimeout() < 420*time.Second {
		t.Fatalf("request timeout %s", cfg.RequestTimeout())
	}
}

func TestPlatformType(t *testing.T) {
	t.Parallel()
	if got := PlatformType("custom"); got != "vllm" {
		t.Errorf("PlatformType(custom) = %q", got)
	}
	if got := PlatformType("llama_cpp"); got != "llama_cpp" {
		t.Errorf("PlatformType(llama_cpp) = %q", got)
	}
}

func TestResolvePlatformURL(t *testing.T) {
	t.Setenv(EnvPlatformURL, "")
	if got := ResolvePlatformURL("", ""); got != DefaultPlatformURL {
		t.Errorf("empty = %q", got)
	}
	if got := ResolvePlatformURL("https://flag.example/", "https://cfg.example"); got != "https://flag.example" {
		t.Errorf("flag = %q", got)
	}
	// Rewritten for DC11: the old localhost:80 placeholder is used as written.
	if got := ResolvePlatformURL("", "http://localhost:80"); got != "http://localhost:80" {
		t.Errorf("localhost config = %q", got)
	}
	t.Setenv(EnvPlatformURL, "https://env.example/")
	if got := ResolvePlatformURL("", "https://cfg.example"); got != "https://env.example" {
		t.Errorf("env = %q", got)
	}
	if got := ResolvePlatformURL("https://flag.example", "https://cfg.example"); got != "https://flag.example" {
		t.Errorf("flag beats env = %q", got)
	}
	cfg := &Config{}
	cfg.Provider.URL = "https://cfg.example"
	ApplyEnvOverrides(cfg)
	if cfg.Provider.URL != "https://env.example" {
		t.Errorf("ApplyEnvOverrides = %q", cfg.Provider.URL)
	}
}

// Plan item Q2, decision DC11: provider_type is no longer a setting in the
// config file. engine is the one setting; the value sent to the node is
// derived from it.
func TestASavedConfigHasNoProviderTypeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := &Config{}
	in.Provider.Engine = "llama_cpp"
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "provider_type") {
		t.Fatalf("the saved config still has a provider_type line:\n%s", raw)
	}
}

// A provider_type line left in an old file is ignored: the engine comes from
// engine alone (as it effectively did before, when the loader overwrote it).
func TestALeftoverProviderTypeLineIsIgnored(t *testing.T) {
	cases := []struct{ yaml, wantEngine string }{
		{"provider:\n  provider_type: llama_cpp\n", "vllm"},
		{"provider:\n  engine: llama_cpp\n  provider_type: vllm\n", "llama_cpp"},
		{"provider:\n  engine: vllm-metal\n  provider_type: llama_cpp\n", "vllm-metal"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Provider.Engine != tc.wantEngine {
			t.Errorf("%q: engine = %q, want %q", tc.yaml, cfg.Provider.Engine, tc.wantEngine)
		}
	}
}

// DC11: the platform address in the config is used as written; the old
// special case that swapped http://localhost:80 for production is gone.
func TestAConfiguredPlatformAddressIsUsedAsWritten(t *testing.T) {
	t.Setenv(EnvPlatformURL, "")
	for _, url := range []string{"http://localhost:80", "http://127.0.0.1:8080", "https://staging.example"} {
		if got := ResolvePlatformURL("", url); got != url {
			t.Errorf("ResolvePlatformURL(%q) = %q, want it as written", url, got)
		}
	}
}

// Plan item Q2, finding QUAL-3: the timeouts in effect, pinned before the
// defaults are tidied so the cleanup cannot change them. A value below the
// floor is raised to it.
func TestTheEffectiveTimeouts(t *testing.T) {
	cases := []struct {
		name                  string
		yaml                  string
		request, llm, session time.Duration
	}{
		{"nothing set", "provider:\n  engine: vllm\n", 420 * time.Second, 330 * time.Second, 90 * time.Second},
		{"below the floors", "server:\n  request_timeout_seconds: 240\n  session_queue_wait_seconds: 30\nprovider:\n  llm_timeout_seconds: 120\n", 420 * time.Second, 330 * time.Second, 30 * time.Second},
		{"above the floors", "server:\n  request_timeout_seconds: 600\nprovider:\n  llm_timeout_seconds: 400\n", 600 * time.Second, 400 * time.Second, 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RequestTimeout() != tc.request || cfg.LLMTimeout() != tc.llm || cfg.SessionQueueWait() != tc.session {
				t.Fatalf("request %v, llm %v, session wait %v; want %v, %v, %v",
					cfg.RequestTimeout(), cfg.LLMTimeout(), cfg.SessionQueueWait(), tc.request, tc.llm, tc.session)
			}
		})
	}
}

// Plan item B1, decision DC4: there is no "unlimited" concurrency. A limit of
// 0 or below stops the client at startup with a message that says what to
// set; leaving the setting out still lets the client decide.
func TestAConcurrencyLimitBelowOneIsRefused(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("server:\n  max_concurrent_inference: "+v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "max_concurrent_inference") {
			t.Errorf("max_concurrent_inference: %s loaded (err %v), want an error naming the setting", v, err)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, explicit := cfg.ExplicitConcurrency(); explicit {
		t.Fatal("a config without the setting reports an explicit limit")
	}
}
