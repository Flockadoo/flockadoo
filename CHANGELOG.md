# Changelog

## [Unreleased]

- A reasoning model (one that thinks before it answers, such as Qwen3, DeepSeek-R1 or gpt-oss) is started with the reasoning parser its approved-catalog entry names (`--reasoning-parser` on vLLM and vLLM Metal), so its thinking is sent apart from its answer. On llama.cpp the entry turns on `--jinja`, which does the same. The parser is saved in `config.yaml` as `reasoning_parser`; an existing install picks it up when `setup` is run again.
- The health report includes the engine (vLLM, vLLM Metal or llama.cpp) and its version, read from the running engine (`/version` on vLLM, `/props` on llama.cpp), so the dashboard shows them on single-node clusters. The local `/api/health` snapshot shows them too.

## [0.43.0] - 2026-11-12

- Installing without the wizard (`INFEROUTE_SKIP_SETUP=1`, and Docker) now needs `PROVIDER_MODEL` and runs `setup`, which checks the model against the approved catalog. `setup` gains `--llm-url` and `--port`.

## [0.42.0] - 2026-11-11

First release.

- Reports GPU temperature and power draw on NVIDIA and Apple Silicon, and GPU memory and utilization on macOS.
- macOS binaries are built natively.
- Errors go to `error.log` instead of the terminal and dashboard.
- Runs vLLM, vLLM Metal, or llama.cpp.
