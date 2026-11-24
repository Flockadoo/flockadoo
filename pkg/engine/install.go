package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Flockadoo/flockadoo/pkg/exechide"
)

const (
	vllmMetalInstallURL = "https://raw.githubusercontent.com/vllm-project/vllm-metal/main/install.sh"
	vllmDocsURL         = "https://docs.vllm.ai/en/stable/getting_started/installation/gpu/index.html"
	// vllmPython is the interpreter every node uses. vLLM's install path, CI,
	// and ROCm wheels are 3.12 (supports 3.11–3.14).
	vllmPython = "3.12"
)

// Install runs the engine's official install path. version pins a vLLM
// release (for example "0.31.0"). Empty installs the current latest.
func Install(ctx context.Context, kind Kind, version string, w io.Writer) error {
	if w == nil {
		w = io.Discard
	}
	switch kind {
	case KindVLLM:
		return installVLLM(ctx, version, w)
	case KindVLLMMetal:
		return installVLLMMetal(ctx, w)
	case KindLlamaCpp:
		return installLlamaCpp(ctx, w)
	default:
		return fmt.Errorf("unknown engine %s", kind)
	}
}

func vllmRequirement(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "vllm"
	}
	return "vllm==" + version
}

func installVLLM(ctx context.Context, version string, w io.Writer) error {
	if runtime.GOOS == "windows" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		return fmt.Errorf("vLLM is not supported on Windows (see %s)", vllmDocsURL)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	venv := filepath.Join(home, ".venv-vllm")
	if strings.TrimSpace(version) != "" {
		if err := replaceVenvDir(w, venv); err != nil {
			return err
		}
	}
	uv, err := ensureUV(ctx, w, home)
	if err != nil {
		return err
	}
	req := vllmRequirement(version)
	_, _ = fmt.Fprintf(w, "Creating vLLM venv at %s (Python %s)...\n", venv, vllmPython)
	// --seed installs pip. FlashInfer's kernel download runs `python -m pip`.
	// --clear replaces a venv left behind by a failed install; uv refuses to
	// recreate one that already exists.
	// --managed-python skips a distro 3.12. Ubuntu 24.04's interpreter reports
	// /usr/include/python3.12, and Triton fails there without python3.12-dev.
	if err := run(ctx, w, uv, "venv", "--python", vllmPython, "--managed-python", "--seed", "--clear", venv); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "Installing vLLM (this can take several minutes)...")
	py := venvExe(venv, "python")
	if err := run(ctx, w, uv, "pip", "install", "--python", py, req); err != nil {
		return fmt.Errorf("vLLM pip install failed: %w (see %s)", err, vllmDocsURL)
	}
	if err := downloadKernels(ctx, w, venv); err != nil {
		return err
	}
	return checkTorchCUDA(ctx, w, venv)
}

// findUV returns uv on PATH, or ~/.local/bin/uv when that file exists.
// pathHit is the result of lookPath("uv"); tests pass it directly.
func findUV(home, pathHit string) string {
	if pathHit != "" {
		return pathHit
	}
	local := filepath.Join(home, ".local", "bin", "uv")
	if runtime.GOOS == "windows" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		local += ".exe"
	}
	if fileExists(local) {
		return local
	}
	return ""
}

// EnsureUV returns the uv program, installing it first when it is missing.
func EnsureUV(ctx context.Context, w io.Writer) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensureUV(ctx, w, home)
}

func ensureUV(ctx context.Context, w io.Writer, home string) (string, error) {
	return ensureUVWith(ctx, w, home, lookPath("uv"), uvSource{})
}

// ensureUVWith is ensureUV with the PATH lookup and the release source given.
func ensureUVWith(ctx context.Context, w io.Writer, home, pathHit string, src uvSource) (string, error) {
	if uv := findUV(home, pathHit); uv != "" {
		return uv, nil
	}
	return installPinnedUV(ctx, w, home, src)
}

// checkTorchCUDA fails setup when the host driver cannot load the CUDA
// runtime bundled in the torch wheel. Otherwise that shows up later, on
// `vllm serve`, after the flock has started waiting.
func checkTorchCUDA(ctx context.Context, w io.Writer, venv string) error {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		_, _ = fmt.Fprintln(w, "Skipping PyTorch CUDA check (nvidia-smi not found).")
		return nil //nolint:nilerr // no NVIDIA driver: the check does not apply
	}
	_, _ = fmt.Fprintln(w, "Checking the NVIDIA driver can load PyTorch...")
	if err := run(ctx, w, venvExe(venv, "python"), "-c", "import torch; torch.cuda.init()"); err != nil {
		return fmt.Errorf("PyTorch cannot use this NVIDIA driver: %w (see %s)", err, vllmDocsURL)
	}
	return nil
}

// downloadKernels fetches prebuilt FlashInfer kernels. Without them vLLM
// compiles missing kernels on first start, which needs nvcc. Machines with a
// driver and no CUDA toolkit die during warmup.
func downloadKernels(ctx context.Context, w io.Writer, venv string) error {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		_, _ = fmt.Fprintln(w, "Skipping vLLM kernel download (nvidia-smi not found).")
		return nil //nolint:nilerr // no NVIDIA driver: no kernels to download
	}
	_, _ = fmt.Fprintln(w, "Downloading FlashInfer kernels...")
	vllm := venvExe(venv, "vllm")
	help, _ := exec.CommandContext(ctx, vllm, "-h").CombinedOutput() //nolint:gosec // G204: vllm is the venv's own executable, which setup installed
	// vLLM 0.33 has no download-kernels subcommand. Later releases wrap this CLI.
	if cliHasSubcommand(string(help), "download-kernels") {
		if err := run(ctx, w, vllm, "download-kernels"); err != nil {
			return fmt.Errorf("vLLM kernel download failed: %w", err)
		}
		return nil
	}
	py := venvExe(venv, "python")
	cmd := exec.CommandContext(ctx, py, "-m", "flashinfer", "download-kernels") //nolint:gosec // G204: py is the venv's own python, which setup installed
	cmd.Stdout = w
	cmd.Stderr = w
	// FlashInfer refuses to import when an older cubin is already installed.
	cmd.Env = append(os.Environ(), "FLASHINFER_DISABLE_VERSION_CHECK=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vLLM kernel download failed: %w", err)
	}
	return nil
}

func venvExe(venv, name string) string {
	if runtime.GOOS == "windows" { //nolint:forbidigo // platform branch in shared code (PLAT-2, item H1)
		return filepath.Join(venv, "Scripts", name+".exe")
	}
	return filepath.Join(venv, "bin", name)
}

func cliHasSubcommand(help, name string) bool {
	for _, field := range strings.FieldsFunc(help, func(r rune) bool {
		switch r {
		case '{', '}', ',', ' ', '\n', '\t', '\r':
			return true
		default:
			return false
		}
	}) {
		if field == name {
			return true
		}
	}
	return false
}

func installVLLMMetal(ctx context.Context, w io.Writer) error {
	if home, err := os.UserHomeDir(); err == nil {
		if err := replaceVenvDir(w, filepath.Join(home, ".venv-vllm-metal")); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(w, "Installing vLLM Metal via official install script...")
	return runScript(ctx, w, vllmMetalInstallURL)
}

func replaceVenvDir(w io.Writer, venv string) error {
	if _, err := os.Stat(venv); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_, _ = fmt.Fprintf(w, "Removing existing venv at %s so uv can recreate it...\n", venv)
	if err := os.RemoveAll(venv); err != nil {
		return fmt.Errorf("remove %s: %w", venv, err)
	}
	return nil
}

func run(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: callers pass the installer's own commands (uv, the venv's python)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// runScript runs a remote install script; tests replace it.
var runScript = runScriptExec

func runScriptExec(ctx context.Context, w io.Writer, url string) error {
	cmd := exec.CommandContext(ctx, "bash", "-c", "curl -fsSL "+url+" | bash") //nolint:gosec // G204: url is this package's fixed vLLM Metal installer URL (vllmMetalInstallURL)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// downloadClient bounds an engine download: a pinned uv or llama.cpp release archive.
var downloadClient = &http.Client{Timeout: 30 * time.Minute}

// versionTimeout bounds `<bin> --version`: a Python engine takes seconds to
// import, and one that hangs must not hang the caller.
const versionTimeout = 20 * time.Second

// InstalledVersion reads `<bin> --version` from stdout (a warning on stderr is
// not the version); the last field is the release. It gives up when ctx ends
// or after versionTimeout. The daemon reads the running engine's version over
// HTTP instead (finding ENG-4); this is for setup and flock workers.
func InstalledVersion(ctx context.Context, bin string) string {
	bin = strings.TrimSpace(bin)
	if bin == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	exechide.Apply(cmd)
	// A child the binary started can hold stdout open after it is killed;
	// stop waiting for it shortly after.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	text := strings.TrimSpace(string(out))
	if err != nil && text == "" {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
