package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReplaceVenvDirRemovesExisting(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv-vllm-metal")
	if err := os.Mkdir(venv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(venv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := replaceVenvDir(&out, venv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(venv); !os.IsNotExist(err) {
		t.Fatalf("venv still exists: %v", err)
	}
	if !strings.Contains(out.String(), venv) {
		t.Fatalf("stdout = %q, want venv path", out.String())
	}
}

func TestCLIHasSubcommand(t *testing.T) {
	help := `usage: vllm [-h] [-v]
            {chat,complete,serve,launch,bench,collect-env,preload,run-batch,snapshot}
            ...`
	if cliHasSubcommand(help, "download-kernels") {
		t.Fatal("vLLM 0.33 help selected download-kernels")
	}
	if !cliHasSubcommand(help, "serve") {
		t.Fatal("serve missing from help")
	}
	newer := "{chat,complete,serve,download-kernels,snapshot}"
	if !cliHasSubcommand(newer, "download-kernels") {
		t.Fatal("download-kernels missing from help")
	}
}

func TestReplaceVenvDirMissingOK(t *testing.T) {
	if err := replaceVenvDir(io.Discard, filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestFindUV(t *testing.T) {
	home := t.TempDir()
	if got := findUV(home, "/usr/bin/uv"); got != "/usr/bin/uv" {
		t.Fatalf("path hit = %q", got)
	}
	if got := findUV(home, ""); got != "" {
		t.Fatalf("missing uv = %q", got)
	}
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "uv"
	if runtime.GOOS == "windows" {
		name = "uv.exe"
	}
	bin := filepath.Join(local, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findUV(home, ""); got != bin {
		t.Fatalf("local uv = %q, want %q", got, bin)
	}
	if got := findUV(home, "/usr/bin/uv"); got != "/usr/bin/uv" {
		t.Fatalf("path hit should win over local, got %q", got)
	}
}

func TestVLLMRequirement(t *testing.T) {
	if vllmRequirement("") != "vllm" || vllmRequirement("  ") != "vllm" {
		t.Fatal("empty pin should install latest")
	}
	if got := vllmRequirement("0.31.0"); got != "vllm==0.31.0" {
		t.Fatal(got)
	}
}

func TestInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "vllm")
	body := "#!/bin/sh\necho vLLM version 0.31.0\n"
	if runtime.GOOS == "windows" {
		// CreateProcess does not honor a shebang. cmd.exe does run .cmd.
		bin += ".cmd"
		body = "@echo vLLM version 0.31.0\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := InstalledVersion(context.Background(), bin); got != "0.31.0" {
		t.Fatalf("version = %q", got)
	}
	if InstalledVersion(context.Background(), "") != "" {
		t.Fatal("empty bin")
	}
}

// Plan item Q4, finding ENG-4: the wizard's --version call has a deadline,
// so a binary that hangs cannot hang the caller.
func TestInstalledVersionGivesUpOnAHungBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "vllm")
	body := "#!/bin/sh\nsleep 30\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		body = "@ping -n 31 127.0.0.1 >nul\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- InstalledVersion(ctx, bin) }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("a hung binary gave version %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InstalledVersion was still waiting for a hung binary after 5s")
	}
}

// A warning on stderr is not the version (finding ENG-4, point 3).
func TestInstalledVersionReadsStdoutOnly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "vllm")
	body := "#!/bin/sh\necho vLLM version 0.31.0\necho 'WARNING: CUDA driver is old' >&2\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		body = "@echo vLLM version 0.31.0\r\n@echo WARNING: CUDA driver is old 1>&2\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := InstalledVersion(context.Background(), bin); got != "0.31.0" {
		t.Fatalf("version = %q, want 0.31.0", got)
	}
}
