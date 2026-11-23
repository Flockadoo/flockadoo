<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/banner-dark.svg">
  <img src=".github/assets/banner-light.svg" alt="Flockadoo: serve open models from your own GPUs and get paid by the Flockadoo network." width="100%">
</picture>

<br>

[![release](https://img.shields.io/github/v/release/Flockadoo/flockadoo?style=for-the-badge&labelColor=2b2f2a&color=d7fd52)](https://github.com/Flockadoo/flockadoo/releases/latest)
[![ci](https://img.shields.io/github/actions/workflow/status/Flockadoo/flockadoo/ci.yml?branch=main&label=ci&style=for-the-badge&labelColor=2b2f2a)](https://github.com/Flockadoo/flockadoo/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-BUSL--1.1-d7fd52?style=for-the-badge&labelColor=2b2f2a)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/Flockadoo/flockadoo?label=go&style=for-the-badge&labelColor=2b2f2a&color=d7fd52)](go.mod)
[![security](https://img.shields.io/badge/security-policy-d7fd52?style=for-the-badge&labelColor=2b2f2a)](SECURITY.md)
[![docker pulls](https://img.shields.io/docker/pulls/flockadoo/flockadoo?style=for-the-badge&labelColor=2b2f2a&color=d7fd52)](https://hub.docker.com/r/flockadoo/flockadoo)

[Quick start](#quick-start-with-installation-script) · [Docker](#-docker-installation) · [Configuration](#-configuration) · [REST API](#-rest-api) · [Docs](https://docs.flockadoo.ai) · [Security](SECURITY.md)

</div>

---


## Supported platforms

| Platform | GPU | LLM backend |
|----------|-----|-------------|
| **Linux + NVIDIA** | Full monitoring via `nvidia-smi` | vLLM |
| **macOS** (Apple Silicon) | Basic info via `system_profiler` | vLLM Metal or llama.cpp |
| **Windows amd64** | `nvidia-smi` when the NVIDIA driver is installed | llama.cpp (GGUF catalog rows) |



## 💻 Hardware Requirements

- **Linux:** At least **32 GB** of system memory and an NVIDIA GPU with **24 GB** of VRAM (for example RTX 3090 or RTX 4090)
- **Windows:** **32 GB** of RAM and an NVIDIA GPU with **24 GB** of VRAM. See the [Windows install documentation](https://docs.flockadoo.ai/introduction/setup-windows).
- **macOS:** Apple Silicon with at least **48 GB** of unified memory, on macOS 13 Ventura or later


## Software Requirements

- **NVIDIA driver** on Linux and Windows so `nvidia-smi` is on `PATH`. No CUDA toolkit is needed.
- **uv** on Linux for vLLM. Setup installs it to `~/.local/bin` if it is missing. No system Python is needed; uv downloads its own Python 3.12 and does not reuse the distro interpreter.


## 💾 Installation

### Quick Start with Installation Script

### Prerequisites
- Visit [www.flockadoo.ai](https://www.flockadoo.ai), sign up and create a flock to obtain your API key. See [Signup Process](https://docs.flockadoo.ai/requirements/signup) for how to create and copy the key from the flock's **Settings**.

#### Linux / macOS one-liner

Works on Linux (amd64/arm64) and macOS (Apple Silicon). Intel Macs are not supported. The wizard asks for your API key, then the engine and model. A Team Flock API key skips that and uses the model chosen for the flock. A join code installs vLLM and confirms the flock at the end:

```bash
curl -fsSL https://raw.githubusercontent.com/flockadoo/flockadoo/main/scripts/install.sh | bash
```

Re-run the wizard anytime: `flockadoo setup`.

Additional information: see the [macOS install documentation](https://docs.flockadoo.ai/introduction/setup-mac).

#### Windows (PowerShell)

Requires 64-bit Windows and an NVIDIA GPU. The wizard offers llama.cpp.

```powershell
irm https://raw.githubusercontent.com/flockadoo/flockadoo/main/scripts/windows-install.ps1 | iex
```

Additional information: see the [Windows install documentation](https://docs.flockadoo.ai/introduction/setup-windows).


## 💫 Install without the wizard

For scripts, CI and Docker, where nobody is there to answer prompts. You run the inference engine yourself and tell the installer which model it serves.

```bash
export FLOCKADOO_SKIP_SETUP=1
export PROVIDER_API_KEY="your-provider-api-key"
export PROVIDER_TYPE="vllm"                       # vllm, vllm-metal or llama_cpp
export PROVIDER_MODEL="Qwen/Qwen2.5-7B-Instruct"  # the model your engine serves
export LLM_URL="http://127.0.0.1:8000"
export SERVER_PORT="8080"

curl -fsSL https://raw.githubusercontent.com/flockadoo/flockadoo/main/scripts/install.sh | bash
```

`PROVIDER_MODEL` is required. It must be an [approved model](https://docs.flockadoo.ai/provider-client/approved-models) for your engine, and your engine must serve it under that exact name. The install stops with an error if it is not. To list the models this machine can serve, run `flockadoo compatibility`.

With a Team Flock key or a join code, leave `PROVIDER_MODEL` unset. The flock's model is used.

The client does not install or start the engine in this mode.

[Override default parameters](https://docs.flockadoo.ai/provider-client/configuration#overriding-defaults)


## 🚀 Launch Flockadoo-client 

**FLOCKADOO Start Command:**
`flockadoo`

**FLOCKADOO Start with specific config:**
`flockadoo --config ~/.config/flockadoo/config.yaml`

**Windows:** `flockadoo` runs in the notification area by default. The prompt waits until the local dashboard is up, then returns. Closing PowerShell does not stop it. Right-click the tray icon → **Open dashboard** for live status in the browser, or **Quit** to stop. Use `flockadoo --console` for the terminal UI.

## Model compatibility check

Works on **Linux + NVIDIA** (`nvidia-smi`), **Windows + NVIDIA** (`nvidia-smi`), and **macOS** (Apple Silicon unified memory via `sysctl` / `system_profiler`). Does **not** start the provider daemon and does not need an API key.

```bash
flockadoo setup
flockadoo compatibility
flockadoo compatibility --provider-type vllm
flockadoo compatibility --json
flockadoo compatibility --catalog-url https://core.flockadoo.ai
flockadoo compatibility --offline-catalog ./approved-models.json
```

Statuses: `runs_well`, `fits`, `tight`, `too_large`, `unknown`. Scoring uses catalog `min_size_bytes` plus a conservative runtime overhead (higher for vLLM). Apple Silicon uses a fraction of unified system RAM; Linux scores against the largest single GPU’s VRAM.

Catalog `engines` further filters the list. On Windows, `compatibility` lists `llama_cpp` rows (GGUF). Untagged vLLM rows stay on Linux and macOS.


## 📦 Docker Installation

The official Flockadoo Docker image flockadoo/flockadoo is available on Docker Hub. 

If the inference engine runs on the Docker host, bind it to `0.0.0.0` so the container can reach it. Set `LLM_URL` to `http://host.docker.internal:8000` (vLLM) or `:8081` (llama.cpp). `host.docker.internal` resolves to the Docker host.

The client listens on `127.0.0.1` inside the container. The client dials out to Flockadoo, so it needs **no** published port. To open the local dashboard from the host, set `server.host` to `0.0.0.0` in the container config and publish the port (for example `-p 127.0.0.1:8080:8080`).

The container always [installs without the wizard](#-install-without-the-wizard), so `PROVIDER_MODEL` is required.

### Docker Quick Start

On a stop the client finishes the requests it is serving, for up to 60 seconds. Docker's default stop timeout is 10 seconds, so the examples give it 75; with less, Docker kills the client mid-answer.

```bash
docker run -d \
  --name flockadoo \
  --stop-timeout 75 \
  -e PROVIDER_API_KEY="your-key" \
  -e PROVIDER_TYPE="vllm" \
  -e PROVIDER_MODEL="Qwen/Qwen2.5-7B-Instruct" \
  -e LLM_URL="http://host.docker.internal:8000" \
  flockadoo/flockadoo:latest
```

### Docker Compose
```yaml
version: '3.8'
services:
  flockadoo:
    image: flockadoo/flockadoo:latest
    environment:
      - PROVIDER_API_KEY=your-key
      - PROVIDER_TYPE=vllm
      - PROVIDER_MODEL=Qwen/Qwen2.5-7B-Instruct
      - LLM_URL=http://host.docker.internal:8000
    stop_grace_period: 75s
    restart: unless-stopped
```

### Build from Source
```bash
docker build -t flockadoo .
docker run -d \
  --name flockadoo \
  --stop-timeout 75 \
  -e PROVIDER_API_KEY="your-key" \
  -e PROVIDER_MODEL="Qwen/Qwen2.5-7B-Instruct" \
  -e LLM_URL="http://host.docker.internal:8000" \
  flockadoo
```


## 💾 Post Installation

When your client first starts it will publish your available models with default costs. 
Please remember to visit flockadoo.ai and change the costs to your preference.

## 🎓 REST API 

Local-only (loopback). Not reachable through the connection to Flockadoo.

- **GET /**: Local status dashboard (same information as the terminal UI). Auto-refreshes in the browser.
- **GET /api/status**: JSON snapshot of that dashboard (session, models, GPU, recent requests).
- **GET /api/health**: Returns the current health status of the provider, including GPU information (if available) and available LLM models.
- **GET /api/busy**: Returns whether the GPU is currently busy (TRUE or FALSE).


## 📝 Configuration

The configuration file (`config.yaml`) contains the following settings:

- **server**: Server configuration (port, host) to access rest API's. 
- **provider**: Provider configuration (API key, central system URL)
  - **provider_type**: Platform provider type. `vllm` for vLLM and vLLM Metal. `llama_cpp` for llama.cpp.
  - **engine**: Local binary: `vllm`, `vllm-metal`, or `llama_cpp`
  - **llm_url**: URL of the local engine (default: `http://127.0.0.1:8000`; llama.cpp: `http://127.0.0.1:8081`)
- **logging**: Logging configuration
  - **level**: Log level (debug, info, warn, error)
  - **log_dir**: Directory where logs are stored (defaults to ~/.local/state/flockadoo/log)
  - **max_size**: Maximum size of log files in megabytes before rotation (default: 100)
  - **max_backups**: Maximum number of old log files to retain (default: 5)
  - **max_age**: Maximum number of days to retain old log files (default: 30)

**Log file locations** (under `log_dir`, default `~/.local/state/flockadoo/log`):

- **flockadoo.log** — main application log (all levels)
- **error.log** — error-level entries only




