# Changelog

## [0.44.1] - 2026-11-20

- After a restart, the first health report waits (up to a minute) for the model's verification answer that is already being fetched. It used to go out without one, so Flockadoo recorded the model as failed and the machine as unavailable until the next report, three minutes later. A report for a model that already has an answer never waits.
- Hashing the weight files no longer holds up requests or health reports. It used to run under the verifier's lock, so while a large `.bin` was hashed (about 20 seconds for a 2 GB file) every request waited for it.

## [0.44.0] - 2026-11-20

- Inferoute is now Flockadoo, Inferscale is now Winglink, and a provider's cluster is now a flock: a Solo Flock is one machine, a Team Flock is a master and its workers. The binary is `flockadoo` (`flockadoo setup`, `flockadoo update`); the config is `~/.config/flockadoo/config.yaml`, where `provider.cluster_mode` is now `provider.flock_role`; the dashboard and the console say FLOCK STATUS, Platform URL and Engine. The client talks to `core.flockadoo.ai`, with the `X-Flockadoo-*` headers and a `flockadoo/<version>` User-Agent, and joins a Team Flock over Winglink (`winglink`, `winglinkd`, interface `winglink0`). Nothing keeps the old names: a machine on the old client is reinstalled.
- Winglink, the network a flock runs on, is now installed from one pinned release, and every file is checked against the SHA-256 built into this client before it is installed or run. A download that does not match is deleted and joining a flock stops with an error; an installed binary that does not match the pin is replaced. `WINGLINK_VERSION` no longer changes which release is used. Until this build pins a release, joining a flock stops with "update the client".
- A model stays served through a short Flockadoo outage. Verification is refreshed in the background every 10 minutes; when Flockadoo cannot be asked, the last "verified" answer is used for up to 30 minutes more, after which requests get 503 ("temporarily unavailable"), never 403. A "not verified" answer still refuses at once, and new or changed weights are never served on an old answer. An outage is no longer reported to Flockadoo as a failed verification, and serving a request no longer waits on a verification call.
- Stopping the client (Ctrl+C, `docker stop`, a service stop) now lets the requests it is serving finish, for up to 60 seconds, after telling Flockadoo it takes no more; a second Ctrl+C stops at once. The Docker examples give the container 75 seconds to stop.
- When the engine stops answering, the client tells Flockadoo it is not accepting requests within about ten seconds, and accepts again as soon as the engine answers; the dashboard says "engine down". The client does not restart the engine. The client also reports not accepting while its connection to Flockadoo is down.
- A client whose API key is rejected, or that Flockadoo asks to update, now stays running and shows why on the dashboard and the console, and stops reporting to Flockadoo, instead of exiting at startup or silently going quiet later. It does the same when Flockadoo says another client has taken over the key. When the connection keeps ending within a minute, the dashboard says another client may be using the same API key.
- A connection to Flockadoo that stops answering without closing is noticed within about 25 seconds and reopened.
- When the engine's answer breaks off partway, the client now aborts the stream to Flockadoo instead of ending it cleanly, so a cut-off answer can never pass for a complete one. Each request on the dashboard shows how it ended (completed, refused_busy, engine_failed, stalled and so on), and only completed requests count as successes.
- An engine that stops sending for three minutes during a streamed answer, including before its first word, is cut off and its slot freed, instead of holding the slot until Flockadoo gave up. A non-streaming answer sends nothing until it is finished, so this limit does not apply to it; the engine timeout (`llm_timeout_seconds`, at least 330 seconds) and Flockadoo's own limit still bound it.
- A request body larger than 16 MiB is refused before it reaches the engine. Streaming is read from the request's own `stream` field, not by searching the body.
- `max_concurrent_inference` must be 1 or more. `0` (which meant unlimited) or a negative value now stops the client at startup with a message saying what to set. Leaving it out still lets the client measure the limit.
- Returning turns of a conversation that wait for a free slot are now served in the order they arrived, and as soon as a slot frees (they used to check every 100 ms and the quickest won). At most 32 wait at once (`server.session_queue_limit`); a turn beyond that is refused at once so Flockadoo can send it elsewhere. When the concurrency benchmark starts, waiting turns are refused at once rather than waiting it out.
- The local web server (`127.0.0.1:8080` by default) no longer answers `/v1/chat/completions`, `/v1/completions` or `/v1/embeddings`. Inference from Flockadoo arrives on the client's own connection to Flockadoo, as before; to use your model yourself, talk to your engine directly. The dashboard, `/api/status`, `/api/health` and `/api/busy` are unchanged.
- The client no longer asks Flockadoo to confirm each request (the `validate_hmac` call). A request that arrives on the connection the client opened with its key comes from Flockadoo; requests are now served even while Flockadoo's API is briefly unreachable, and each one is answered without that round trip.
- The deployment fingerprint reads the engine's version from the running engine instead of running `<engine> --version` once a minute. An engine upgrade now counts as a new deployment, so the concurrency benchmark and the capability checks run again. The fingerprint changes once after this update, which re-runs them once.
- An engine `/metrics` value that is out of range, NaN or infinite (a KV cache usage above 100%, a negative queue) is now left out of the capacity report instead of being sent. A NaN used to make the whole report fail, so it was never sent.
- A stream time limit from Flockadoo larger than 24 hours is capped at 24 hours. A very large value used to overflow and end every write to that stream at once.
- A model file whose safetensors header claims an impossible size (more than the format's 100 MB limit) is now refused with an error during verification. It used to crash the client.
- `provider_type` is no longer a setting in `config.yaml`: `engine` is the one setting, and the provider type sent to Flockadoo (vllm, or llama_cpp) is worked out from it, as before. A `provider_type` line left in an older file is ignored, which is what already happened in effect, and `setup` no longer writes one.
- A platform address of `http://localhost:80` in `config.yaml` is now used as written. It used to be replaced with the production address; a file that still holds it will not connect until `url` is corrected or `setup` is run again.
- The Docker image no longer contains `cloudflared`, which nothing used.
- Reconnecting to Flockadoo is fast again after a long run. The wait before a reconnect used to double on every disconnect and never come back down, so a client that had dropped six times waited 32 to 48 seconds before every reconnect for the rest of its life. It now starts again from one second after a connection that lasted a minute. A connection that drops sooner still backs off, so two clients started with the same key do not knock each other off at full speed.
- A reasoning model (one that thinks before it answers, such as Qwen3, DeepSeek-R1 or gpt-oss) is started with the reasoning parser its approved-catalog entry names (`--reasoning-parser` on vLLM and vLLM Metal), so its thinking is sent apart from its answer. On llama.cpp the entry turns on `--jinja`, which does the same. The parser is saved in `config.yaml` as `reasoning_parser`; an existing install picks it up when `setup` is run again.
- A reasoning model gets a sixth capability check: the client asks it to think and reports whether the thinking comes back apart from the answer. The inference node serves a reasoning model only from providers that pass. Other models skip the check. Turning a tool or reasoning parser on now counts as a new deployment, so the checks run again.
- The health report includes the engine (vLLM, vLLM Metal or llama.cpp) and its version, read from the running engine (`/version` on vLLM, `/props` on llama.cpp), so the dashboard shows them on Solo Flocks. The local `/api/health` snapshot shows them too.

## [0.43.0] - 2026-11-12

- Installing without the wizard (`INFEROUTE_SKIP_SETUP=1`, and Docker) now needs `PROVIDER_MODEL` and runs `setup`, which checks the model against the approved catalog. `setup` gains `--llm-url` and `--port`.

## [0.42.0] - 2026-11-11

First release.

- Reports GPU temperature and power draw on NVIDIA and Apple Silicon, and GPU memory and utilization on macOS.
- macOS binaries are built natively.
- Errors go to `error.log` instead of the terminal and dashboard.
- Runs vLLM, vLLM Metal, or llama.cpp.
