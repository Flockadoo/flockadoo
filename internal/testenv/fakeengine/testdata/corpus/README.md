# Stream corpus

Real streamed output from inference engines, recorded byte for byte with `scripts/record_sse.sh`. Each folder is one run of the script; `meta.txt` says when and on what. Never edit a recording, and never add one written by hand (AGENTS.md, "Engines and the stream corpus").

| Folder | Engine | Model | Served with |
|---|---|---|---|
| `vllm-0.28.0-qwen2.5-3b-instruct` | vLLM 0.28.0 (vllm-metal, Mac mini M4) | Qwen/Qwen2.5-3B-Instruct | `--max-model-len 16384 --enable-auto-tool-choice --tool-call-parser hermes` |
| `vllm-0.28.0-qwen3-1.7b-reasoning` | vLLM 0.28.0 (vllm-metal, Mac mini M4) | Qwen/Qwen3-1.7B | `--max-model-len 16384 --enable-auto-tool-choice --tool-call-parser hermes --reasoning-parser qwen3` |
| `llamacpp-b12359-qwen2.5-3b-instruct` | llama.cpp b12359 (b1b770d90, Homebrew, Mac mini M4) | Qwen/Qwen2.5-3B-Instruct, `qwen2.5-3b-instruct-q4_k_m.gguf` | `-ngl 99 -c 16384 --jinja --metrics` |
| `llamacpp-b12359-qwen3-1.7b-reasoning` | llama.cpp b12359 (b1b770d90, Homebrew, Mac mini M4) | Qwen/Qwen3-1.7B, `Qwen3-1.7B-Q8_0.gguf` | `-ngl 99 -c 16384 --jinja --metrics` |

All four were recorded on 2026-11-16. vLLM sends thinking as `delta.reasoning`, llama.cpp as `delta.reasoning_content`. In the reasoning runs most cases spend their `max_tokens` thinking and end with `finish_reason: "length"` and no answer; `chat-long` goes from thinking to an answer, and the two-call tool cases go from thinking to tool calls. The llama.cpp folders have no `props.json`: llama-server's reply holds the model's local file path, which names the recording machine's user.
