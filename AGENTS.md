# Working on flockadoo

The provider's half of Flockadoo: one Go binary that relays inference between the platform's gateway tunnel and a local engine (vLLM, vLLM Metal or llama.cpp), with reporters around it (health, capacity, capability probes, concurrency benchmark), a setup wizard, a self-updater and the multi-machine cluster code. The client never interprets a response body; it copies the engine's bytes. Everything it reports to the node is a claim, not proof.

What each feature is and where it lives: `FEATURES.yaml`. The audit and refactor plan: `flockadoo-plans/client_reliability_audit_3d9b52e7.plan.md` (findings, invariants I1 to I14 in section 13, the target packages in section 14, the backlog in section 20).

## Commands

Run this before you report a change as done. CI runs exactly this on Linux, macOS and Windows:

```bash
go run ./internal/tools/check     # gofmt, go mod tidy, vet for three targets, lint, race tests, feature map
go test ./pkg/server/             # one package
```

The check needs golangci-lint v2.15.0 (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`; PATH or GOPATH/bin). Lint judges the whole codebase (item L1 cleared the old findings); `-lint-base <rev>` judges only what changed since the merge base with that revision. The tests run with HOME pointed at an empty temporary directory, and the check fails if a test leaves a file there.

## Where code goes

Today's packages are under `pkg/`; they move to `internal/` as the backlog item that reworks each one lands (plan section 14). The target:

- `internal/admission`: the concurrency gate and slots. `internal/tunnel`: the gateway session. `internal/engine`: talking to a running engine. `internal/relay`: the inference handler. These are the request path.
- `internal/platform`: every call to the node, with request and response types and typed errors.
- `internal/health`, `capacity`, `capability`, `benchmark`, `verify`, `catalog`: the reporters.
- `internal/hardware`, `enginectl` (install and start engines), `cluster` (tailnet, roles, the node-built command): the machine.
- `internal/setup`, `compat`, `update`, `ui`, `download`: operator-facing. `internal/testenv`: the fakes.

A new engine is one adapter in `engine`, one file in `enginectl` and one row in `Kind`. A new capability is one row in the capability table. A new node endpoint is one method in `platform` and one golden in `pkg/contract/testdata`.

## Rules the checks enforce

- `internal/testenv` is imported only from `_test.go` files.
- `exec.CommandContext`, never `exec.Command`. `http.NewRequestWithContext`, never `http.NewRequest`, `http.Get` or `http.DefaultClient`.
- No `context.Background()`, `fmt.Print*`, `os.Stdout`, `os.Getenv` or `runtime.GOOS` in library code. `cmd/` owns the process; the wizard may print; configuration and build-tagged platform files may read the environment.
- No `time.Sleep` outside tests. No `t.Skip` in an invariant test; a platform-specific or real-engine test that skips says why in a `//nolint:forbidigo` comment.
- A `//nolint` needs the linter's name and a reason.
- `FEATURES.yaml` lists code and tests for every feature, and every path exists.

## Rules the checks cannot enforce

- Never log or store a request or response body. Never add a usage number the engine did not send.
- An engine call needs a concurrency slot, released exactly once.
- A new call to the node needs a request golden (and a response golden if the client reads the response).
- A new engine, or an engine version that changes its stream, needs a recording before the adapter changes. Never write a recording by hand.
- The API key, the node token and the tailnet key never appear in logs, the dashboard, the status API or anything sent to the node except the authorization header (I12).
- Model verification identifies a model; it does not prove one. Safetensors are hashed by header only (architecture and tensor layout, not the weights), and a modified client can report anything (finding SEC-2). Nothing may treat the client's verification as proof.

## Never

- Run `setup`, `update` or the cluster code for real; use `sudo`; write outside `t.TempDir()`.
- Contact `core.flockadoo.ai` or any real host from a test. Tests use `httptest` and, after item A2, `internal/testenv`.
- Change a golden or a recording to make a test pass.
- Modify flockadoo-server from this repository. A change the node depends on needs the node-side change agreed first.

## Definition of done

Every task follows six steps. Compiling and passing unit tests is not done.

1. **Understand.** Name the feature in `FEATURES.yaml`, its packages, and the invariants the change can affect.
2. **Plan.** Say what will change and what will not. Name the tests that will show it: the tests that fail before the change and pass after. They are the acceptance.
3. **Test first.** Write the tests before the code, from the spec (the plan's invariants, the contract, the intended behaviour), not from the code. Include a negative case. Run them and watch them fail for the reason the change addresses, not because they do not compile. In the pull request the first commit holds the tests alone, and the description keeps their failing output.
   - A bug fix starts with a test that reproduces the bug.
   - A refactor that must not change behaviour starts with characterization tests that pass before it and still pass after it, unchanged.
   - When a test cannot fail first (the behaviour already holds), say so.
4. **Implement.** The smallest coherent change that makes those tests pass. Do not change a test to make it pass; if the test was wrong, say why and fix it as its own step.
5. **Verify.** `go run ./internal/tools/check`. For the request path or the tunnel, the tests named in the feature map as well.
6. **Review.** Someone or something that did not write the change checks the behaviour and the negative cases.

Report what you verified and on which operating systems, what you did not, and any open risk, with the failing run from step 3 and the passing run from step 5.

Not done: a behaviour change with no test that failed before it and no reason given; only compiled or only unit-tested; a test skipped, deleted or loosened to pass; a test merged while known to fail; a `//nolint` without a reason; verification claimed without output.

## Needs human review

`CODEOWNERS` enforces these. A change that touches: anything that runs as root or with `sudo` (the cluster and tailnet code); anything that downloads or executes (engine installers, the updater, the tailnet binaries); how credentials are stored, sent or shown; a golden, or the request and response types of a call to the node; the concurrency limit and the benchmark's thresholds (they are policy); the CI and release workflows. The pinned Winglink version and checksums (item S1) are stricter still: automation may open that pull request, and only a maintainer merges it.
