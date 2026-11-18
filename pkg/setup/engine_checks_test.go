package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/engine"
)

// skipWithoutVLLM skips where the wizard turns vLLM into llama.cpp: neither a
// YaRN override nor a vLLM release applies there.
func skipWithoutVLLM(t *testing.T) {
	t.Helper()
	if engine.RuntimeKind(runtime.GOOS, engine.KindVLLM) == engine.KindLlamaCpp {
		t.Skip("this OS serves vLLM rows with llama.cpp") //nolint:forbidigo // platform test: Windows has no vLLM to check
	}
}

// yarnCatalog writes an offline catalog with one Qwen2.5 row that asks for
// YaRN to 131072, with rope_theta when theta is not zero.
func yarnCatalog(t *testing.T, theta float64) string {
	t.Helper()
	row := map[string]any{
		"id": "7", "alias": "Qwen/Qwen2.5-7B-Instruct", "service_type": "vllm",
		"display_name": "Qwen2.5 7B", "hf_repo": "Qwen/Qwen2.5-7B-Instruct", "hf_ref": "main",
		"min_size_bytes": 1000000, "is_active": true, "engines": []string{"vllm", "vllm-metal"},
		"max_model_len": 131072, "rope_type": "yarn", "rope_base_context_len": 32768,
	}
	if theta != 0 {
		row["rope_theta"] = theta
	}
	raw, err := json.Marshal(map[string]any{"object": "list", "data": []any{row}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A catalog row with YaRN but no rope_theta (an older node) cannot make a
// working vLLM command, so setup stops and says why.
func TestSetupRefusesACatalogRowWithIncompleteRope(t *testing.T) {
	skipWithoutVLLM(t)
	singlePlan(t)
	dir := t.TempDir()
	err := Execute(Options{
		ConfigPath: filepath.Join(dir, "config.yaml"), Engine: "vllm", Model: "Qwen/Qwen2.5-7B-Instruct",
		APIKey: "k", OfflineCatalog: yarnCatalog(t, 0), Yes: true, NoStart: true,
	}, Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "rope_theta") {
		t.Fatalf("Execute = %v, want a refusal naming rope_theta", err)
	}
}

// With rope_theta the printed command carries the whole rope_parameters dict,
// and the config keeps theta for auto-start.
func TestSetupPrintsTheCompleteYarnOverride(t *testing.T) {
	skipWithoutVLLM(t)
	singlePlan(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	var out, errBuf bytes.Buffer
	err := Execute(Options{
		ConfigPath: cfgPath, Engine: "vllm", Model: "Qwen/Qwen2.5-7B-Instruct",
		APIKey: "k", OfflineCatalog: yarnCatalog(t, 1000000), Yes: true, NoStart: true,
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf})
	if err != nil {
		t.Fatalf("Execute: %v\n%s", err, errBuf.String())
	}
	if !strings.Contains(out.String(), `\"rope_parameters\":{\"factor\":4,\"original_max_position_embeddings\":32768,\"rope_theta\":1000000,\"rope_type\":\"yarn\"}`) {
		t.Fatalf("printed command:\n%s", out.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.RopeTheta != 1000000 {
		t.Fatalf("saved rope_theta = %v", cfg.Provider.RopeTheta)
	}
}

// fakeServeBin is a vllm that reports version and, if anyone runs `serve`,
// leaves a marker file behind.
func fakeServeBin(t *testing.T, version string) (bin, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "served")
	bin = filepath.Join(dir, "vllm")
	body := "#!/bin/sh\nif [ \"$1\" = serve ]; then : > '" + marker + "'; exit 1; fi\necho vLLM version " + version + "\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		body = "@if \"%1\"==\"serve\" (type nul > \"" + marker + "\" & exit /b 1)\r\n@echo vLLM version " + version + "\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, marker
}

// A vLLM below the minimum is replaced by the current release (--yes).
func TestSetupOffersReinstallForAnOldVLLM(t *testing.T) {
	skipWithoutVLLM(t)
	singlePlan(t)
	old, _ := fakeServeBin(t, "0.29.2")
	current, _ := fakeServeBin(t, "0.34.0")
	bin := old
	detectEngine = func(kind engine.Kind) engine.Detected {
		return engine.Detected{Kind: kind, Found: true, Bin: bin}
	}
	installs := 0
	installEngine = func(_ context.Context, _ engine.Kind, version string, _ io.Writer) error {
		installs++
		if version != "" {
			t.Errorf("installed %q, want the current release", version)
		}
		bin = current
		return nil
	}
	dir := t.TempDir()
	var out, errBuf bytes.Buffer
	err := Execute(Options{
		ConfigPath: filepath.Join(dir, "config.yaml"), Engine: "vllm", Model: "Qwen/Qwen3-0.6B",
		APIKey: "k", OfflineCatalog: filepath.Join("testdata", "catalog.json"), Yes: true, NoStart: true,
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf})
	if err != nil {
		t.Fatalf("Execute: %v\n%s", err, errBuf.String())
	}
	if installs != 1 {
		t.Fatalf("installs = %d, want 1\n%s", installs, out.String())
	}
	if !strings.Contains(out.String()+errBuf.String(), "needs vLLM 0.30.0 or newer") {
		t.Fatalf("output does not name the minimum:\n%s\n%s", out.String(), errBuf.String())
	}
}

// Declining the reinstall leaves the old vLLM alone, turns auto-start off and
// never starts it.
func TestDeclinedReinstallTurnsAutoStartOff(t *testing.T) {
	skipWithoutVLLM(t)
	singlePlan(t)
	old, marker := fakeServeBin(t, "0.29.2")
	detectEngine = func(kind engine.Kind) engine.Detected {
		return engine.Detected{Kind: kind, Found: true, Bin: old}
	}
	installEngine = func(context.Context, engine.Kind, string, io.Writer) error {
		t.Fatal("installed after the operator said no")
		return nil
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	var out, errBuf bytes.Buffer
	err := Execute(Options{
		ConfigPath: cfgPath, Engine: "vllm", Model: "Qwen/Qwen3-0.6B",
		APIKey: "k", OfflineCatalog: filepath.Join("testdata", "catalog.json"),
	}, Streams{In: strings.NewReader("n\n"), Out: &out, Err: &errBuf})
	if err != nil {
		t.Fatalf("Execute: %v\n%s", err, errBuf.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.AutoStart {
		t.Fatal("auto_start is on for a vLLM below the minimum")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("setup started the old vLLM")
	}
	if !strings.Contains(errBuf.String(), "will not serve") {
		t.Fatalf("stderr does not say the client will not serve:\n%s", errBuf.String())
	}
}

// A cluster pinned below the minimum would install a release every client
// refuses, so setup stops before installing or joining.
func TestSetupRefusesAClusterPinBelowTheMinimum(t *testing.T) {
	skipWithoutVLLM(t)
	singlePlan(t)
	clusterPlan = func(*config.Config) (clusterSettings, error) {
		return clusterSettings{
			Role: "worker", Name: "inferoute-cluster1",
			Model: "Qwen/Qwen3-0.6B", ServiceType: "vllm", VLLMVersion: "0.29.2",
		}, nil
	}
	detectEngine = func(kind engine.Kind) engine.Detected {
		return engine.Detected{Kind: kind}
	}
	installEngine = func(context.Context, engine.Kind, string, io.Writer) error {
		t.Fatal("installed a release below the minimum")
		return nil
	}
	dir := t.TempDir()
	err := Execute(Options{
		ConfigPath: filepath.Join(dir, "config.yaml"), APIKey: "join-abc",
		OfflineCatalog: filepath.Join("testdata", "catalog.json"),
	}, Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "0.30.0 or newer") {
		t.Fatalf("Execute = %v, want the minimum named", err)
	}
}
