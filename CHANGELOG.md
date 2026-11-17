# Changelog

## [Unreleased]

- `provider_type` is no longer a setting in `config.yaml`: `engine` is the one setting, and the provider type sent to Inferoute (vllm, or llama_cpp) is worked out from it, as before. A `provider_type` line left in an older file is ignored, which is what already happened in effect, and `setup` no longer writes one.
- A platform address of `http://localhost:80` in `config.yaml` is now used as written. It used to be replaced with the production address; a file that still holds it will not connect until `url` is corrected or `setup` is run again.
- The Docker image no longer contains `cloudflared`, which nothing used.
- Reconnecting to Inferoute is fast again after a long run. The wait before a reconnect used to double on every disconnect and never come back down, so a client that had dropped six times waited 32 to 48 seconds before every reconnect for the rest of its life. It now starts again from one second after a connection that lasted a minute. A connection that drops sooner still backs off, so two clients started with the same key do not knock each other off at full speed.
- A reasoning model (one that thinks before it answers, such as Qwen3, DeepSeek-R1 or gpt-oss) is started with the reasoning parser its approved-catalog entry names (`--reasoning-parser` on vLLM and vLLM Metal), so its thinking is sent apart from its answer. On llama.cpp the entry turns on `--jinja`, which does the same. The parser is saved in `config.yaml` as `reasoning_parser`; an existing install picks it up when `setup` is run again.
- A reasoning model gets a sixth capability check: the client asks it to think and reports whether the thinking comes back apart from the answer. The inference node serves a reasoning model only from providers that pass. Other models skip the check. Turning a tool or reasoning parser on now counts as a new deployment, so the checks run again.
- The health report includes the engine (vLLM, vLLM Metal or llama.cpp) and its version, read from the running engine (`/version` on vLLM, `/props` on llama.cpp), so the dashboard shows them on single-node clusters. The local `/api/health` snapshot shows them too.

## [0.43.0] - 2026-11-12

- Installing without the wizard (`INFEROUTE_SKIP_SETUP=1`, and Docker) now needs `PROVIDER_MODEL` and runs `setup`, which checks the model against the approved catalog. `setup` gains `--llm-url` and `--port`.

## [0.42.0] - 2026-11-11

First release.

- Reports GPU temperature and power draw on NVIDIA and Apple Silicon, and GPU memory and utilization on macOS.
- macOS binaries are built natively.
- Errors go to `error.log` instead of the terminal and dashboard.
- Runs vLLM, vLLM Metal, or llama.cpp.
