# JEV provider for CLIProxyAPI

Adds native JEV decisions from TypeSafe, OpenRouter, and OpenCode Zen. CLIProxyAPI selects the credential and handles routing, priorities, retries, failover, and cooldowns. This plugin declares auth-provider, model-provider, executor, and management-resource capabilities. The management capability serves static tester assets only. Each decision execution sends one request through `host.http.do` using the selected credential context.

## Repository and subtree workflow

The plugin repository is `https://github.com/abdwhb-png/cliproxy-jev-provider`, matching this module's path and registration metadata. Its branch is `main`. `cliproxy-deployment` consumes it as a squashed Git subtree at `plugins/cliproxy-jev-provider/`; changing directories into that prefix still operates on the parent Git checkout. CLIProxyAPI is a separate Git submodule and must not use this workflow.

Commit plugin source, tests, and this README in a commit limited to the prefix. Commit parent deployment configuration, instructions, and ADR/notes separately. After the intended plugin changes are committed and the worktree is clean, publish from the parent root with:

```bash
git subtree push --prefix=plugins/cliproxy-jev-provider \
  https://github.com/abdwhb-png/cliproxy-jev-provider.git main
```

Verify the resulting remote branch before reporting success. Import updates with `git subtree pull --prefix=plugins/cliproxy-jev-provider https://github.com/abdwhb-png/cliproxy-jev-provider.git main --squash`, preserving local work and inspecting any divergence before pulling. Publication does not publish deployment files or activate a rebuilt runtime library. The relative SDK replacement assumes the deployment checkout layout: the plugin is under `plugins/` and the SDK is at root `CLIProxyAPI/`. Deployment-specific ADRs and qualification reports referenced below live in the private parent repository; they are not included in the plugin repository.

## Browser decision testing

Version `0.2.0` adds **JEV Decision Tester** to the WebUI's plugin navigation after an approved reload or restart. It also opens directly at `/v0/resource/plugins/cliproxy-jev-provider/tester` on the proxy's address. The page uses the same origin as the proxy; it supports the deployed WebUI served at `/management.html` without frontend or host source changes.

1. Enter a **CLIProxyAPI client API key**, not the management password or an upstream provider key.
2. Click **Load models / Refresh**. Available JEV aliases come from the authenticated `/v1/models` catalog, filtered by `owned_by: typesafe`. The current deployed host omits `type` and `supported_parameters`, so the page does not depend on those fields. Excluded or unavailable models are not invented by the tester.
3. Load a synthetic `noul`, `choice`, `score`, or combined example, or paste your full native request. Examples prefer the available `jev-1.13-free` alias; edit `model` to select another available alias. Loading an example replaces the editor contents and does not submit a request.
4. Click **Run decision**. Inspect HTTP status, elapsed time, and the complete native response. **Cancel** aborts the request; changing the client key also cancels pending work and clears the catalog.

The client key lives only in page memory and is cleared on reload or navigation. The page writes neither credentials nor request history to browser storage and does not read the WebUI's saved login. Its static assets are public; model discovery and execution still require the normal client key. Requests are restricted to the same origin and responses are rendered as text.

The browser checks JSON syntax and that `model` is an available JEV alias, then submits the editor text unchanged to `/v1/chat/completions`. Native schema validation stays in the existing executor; structured state, additional fields, duplicate-key errors, and large integers are not hidden by browser reserialization. The complete response is displayed unchanged, including native metadata and upstream error responses. The existing WebUI provider chat test remains unsuitable because it sends `messages` rather than decision JSON.

## Request contract

Send native decision JSON to `POST /v1/chat/completions`, authenticated with the deployment's client API key:

```json
{
  "model": "or/jev-1.13",
  "state": {"ticket": "Payments have failed for three days."},
  "questions": {
    "urgent": {"type": "noul", "instructions": "Does this require urgent attention?"},
    "team": {
      "type": "choice",
      "instructions": {"question": "Which team should handle this?"},
      "criteria": {"billing": "Payment problems", "support": "Other problems"}
    },
    "severity": {
      "type": "score",
      "instructions": "How severe is this?",
      "criteria": ["Minor inconvenience", "Work blocked", "Critical outage"]
    }
  }
}
```

The response is native JEV JSON containing `model`, `answers`, and `usage`; it is returned without a chat `choices` wrapper. Additional upstream metadata, including OpenRouter `id`, `provider`, and cost, remains intact. Use an HTTP client that understands decision JSON, rather than a chat SDK that requires `choices`.

`state`, instructions, and criteria retain structured JSON and large integers. Questions are caller-owned: the plugin never invents questions, answers, thresholds, or confidence values. It validates question types and rubrics before contacting the upstream and validates answer types, rubric membership, numeric bounds, and token counts before returning success. `noul` criteria can specify `true`/`false`; choice has 1–255 options; score has 2–10 ordered levels. Native fields outside those checks pass through. `stream: false` is accepted and removed before forwarding. Streaming, ordinary chat/tool requests, Responses, and Anthropic Messages are rejected.

## Models and host routing

| Public model | Eligible upstreams | Selected upstream model |
| --- | --- | --- |
| `jev-1.13` | TypeSafe, OpenRouter, Zen paid | Determined from the host-selected credential |
| `typesafe/jev-1.13` | TypeSafe | `jev-1.13.0` |
| `or/jev-1.13` | OpenRouter | `typesafe/jev-1.13` |
| `ocz/jev-1.13` | Zen paid | `jev-1.13` |
| `jev-1.13-free`, `ocz/jev-1.13-free` | Zen free | `jev-1.13-free` |

Pins use the `prefix` in the example credential files. Keep the host's `force-model-prefix` disabled to retain shared aliases. Models appear only for valid, enabled records with available environment keys. Without a record, that upstream contributes no models; the plugin publishes no static phantom models.

The shared paid alias follows the existing host scheduler (`fill-first` in this deployment). The free model is excluded from that alias. Standard auth metadata such as `priority`, `disabled`, and `excluded_models` is preserved for the host. No JEV-specific weighting rules are introduced. The host provider identity is `jev`; `jev_upstream` distinguishes the upstream vendor. Do not assume existing chat scheduler rules apply to this identity.

JEV auth IDs and cooldowns are separate from OpenRouter/Zen chat IDs, even when they reference the same underlying API key. An upstream failure retains its HTTP status with a sanitized message; raw error bodies are not exposed. **RPC schema 6 cannot carry upstream error headers such as `Retry-After`.** The host therefore applies its default status-based retry/cooldown policy. This limitation is accepted in ADR-0005 (`docs/adr/0005-auth-bound-jev-decision-plugin.md` in the deployment repository).

## Credentials and configuration

The [example auth records](examples/auths) contain environment variable names, never credential values. Each file declares `type: jev`, one `upstream`, and `api_key_env`. Optional `headers_env` references extra header values, such as the existing Zen session header; empty optional header values are omitted. Authentication and transport headers cannot be overridden this way. Optional `label`, `prefix`, and `proxy_url` use the host auth fields.

Set keys through the root `.env` secure channel. The root Compose `env_file` passes them into the host process. Existing `OR_API_KEY_ONE`, `OR_API_KEY_TWO`, and `OPENCODE_KEY_CREDITS` are reused; `.env.example` adds a blank `TYPESAFE_API_KEY`. Never put literal key values into auth records. To add another credential, create another file referring to a different environment variable; do not create multiple records for the same key.

After runtime approval, copy only the desired example files to root `auths/`, preserving existing files. For example, from the deployment root:

```bash
cp -n plugins/cliproxy-jev-provider/examples/auths/jev-openrouter-one.json auths/
```

Do not blindly overwrite auth files: file identity is host credential identity. The host watches auth files; environment changes require an approved process/container recreation. Refresh keeps environment references rather than writing resolved keys. Key lookup occurs during discovery and execution. Missing or invalid required keys produce an explicit error; such records contribute no models.

Root `config.template.yaml` configures:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cliproxy-jev-provider:
      enabled: true
      priority: 80
```

Defaults are TypeSafe `https://api.typesafe.ai/v1/systemone`, OpenRouter `https://openrouter.ai/api/v1/systemone`, and Zen `https://opencode.ai/zen/v1/systemone`. Optional `endpoints` maps known vendors to absolute HTTP(S) decision URLs. Overrides cannot embed credentials, queries, or fragments. Unknown config fields, invalid URLs, and multiple YAML documents are rejected; a rejected update leaves the previous endpoint snapshot intact. Host-owned `enabled` and `priority` are accepted but do not implement routing policy.

## Build and verification

This standalone Go module builds against the local CLIProxyAPI `/v8` SDK through `replace ../../CLIProxyAPI`. The verified checkout is `b16461bd7c2ac5333f669dbc2330fe34a56dfb0e` (ABI 1, RPC schema 6). Its local replacement version is not a published SDK release. No fork source edits are required. Both modules must be present; use the deployment's recursive submodule checkout. Build requires the established Go 1.26 toolchain and a Linux amd64 C compiler. Dependencies are locked; Make targets use offline module resolution.

```bash
make -C plugins/cliproxy-jev-provider fmt
make -C plugins/cliproxy-jev-provider verify
make -C plugins/cliproxy-jev-provider build
nm -D plugins/cliproxy-jev-provider/dist/cliproxy-jev-provider.so | rg 'cliproxy_plugin_init$'
```

`test` exercises real RPC handlers; `race` covers concurrent execution/reconfiguration; `transport` builds the actual host and `.so`, then uses temporary auth/config files, fake keys, loopback upstreams, and a guard proxy. It covers all native IDs and aliases, resource routes, host priorities, disabled/missing/invalid credentials, failover, cooldowns, cancellation, native-body preservation, usage accounting, and unrelated chat execution. It requires local socket access. Set `JEV_TEST_HOST_BINARY` to reuse a host build; use the checkout's binary for source qualification and the deployed binary for compatibility qualification. Set `GOCACHE` to a writable location when needed.

`browser` runs that same host fixture with an isolated `agent-browser` session. It requires an installed `agent-browser` CLI and supported Chrome/Chromium browser; no JavaScript package installation is needed. It exercises the served production page, invalid authentication, aliases, examples, validation failures, unchanged structured JSON and large integers, literal HTML-like response rendering, upstream errors, duplicate-submit prevention, cancellation, key-change cancellation, and clearing credentials on reload. `verify` includes browser verification, race, vet, and module verification. `transport` remains available for environments without browser tooling; it does not qualify the browser UI.

The root Compose mount is read-only with `create_host_path: false`; build all three plugins before service startup. `dist/` is ignored. A source rebuild does not replace an already loaded native library.

To prepare a candidate without replacing the mounted artifact, run `make build OUTPUT=dist/candidate/cliproxy-jev-provider.so`. Runtime promotion requires separate approval; preserve the config volume and ensure `RESET_CONFIG=0`. Updating this already active provider to `0.2.0` needs no credential or routing migration.

### Local verification record — 2026-09-30

RPC tests, the full Go race suite, `go vet`, and `go mod verify` passed. The Linux amd64 native library built and exports `cliproxy_plugin_init`. The real host loopback passed, including an assertion that its configured proxy was used. Focused host tests for plugin HTTP-status preservation and auth-manager usage publication passed. Root template YAML and Compose mount validation passed. The CLIProxyAPI checkout has no new source changes.

LSP diagnostics were not run because `gopls` is unavailable. Compiler and vet checks ran. Authenticated probes on 2026-09-30 passed for TypeSafe, both OpenRouter credentials, and Zen free, validating `noul`, `choice`, and `score`. Zen paid returned HTTP 402 with `Upstream request failed: Insufficient account funds` and remains unqualified. See the probe report (`docs/notes/jev-authenticated-provider-probes.md` in the deployment repository).

Approved runtime activation on 2026-09-30 passed live checks for all three qualified provider pins and the shared alias. All 133 original public model IDs remain, with five JEV aliases added. Paid Zen is excluded in its runtime auth record; usage statistics retain their previous disabled setting. See the activation report (`docs/notes/jev-runtime-activation.md` in the deployment repository), including the reset-flag recovery and remaining verification limits.

### Browser verification record — 2026-10-01

Registration and resource RPC tests followed RED → GREEN. A real browser test first failed on missing page controls, then passed against the implemented page. Native host and browser checks passed both with the checked-out SDK host and with an isolated copy of the currently deployed host binary (`sha256:b7c8ddc9c18e25ad71287d5269ae908448049027a8c13e1bd86251f6cfc9c6f2` image).

`make verify` passed: RPC tests, race detector, vet, native host/browser integration, and module verification. JavaScript syntax and Git diff checks passed. These isolated browser checks used synthetic requests, fake credentials, and guarded local upstreams. `gopls` is unavailable, so no LSP diagnostics ran.

The user subsequently approved promotion of `0.2.0`. The live standalone tester passed authenticated model discovery and four browser decision requests: TypeSafe, OpenRouter, Zen free, and the shared alias, each with all three question types. The existing image, exact persisted configuration, and all 138 model IDs were preserved. Invalid authentication, frontend validation, credential clearing on reload, and a 390-pixel layout were also checked. Live navigation through the management menu remains unverified because the session has no plaintext management credential; the standalone tester is usable with the client API key. See the live browser report (`docs/notes/jev-browser-activation.md` in the deployment repository).

## Activation boundary

Source, template, and mount preparation do not activate the plugin. The persisted config volume is preserved on ordinary startup. Before promotion, obtain runtime approval, back up active config/auths, provision selected records, remove the old `typesafe AI console` compatibility block from active config, add the plugin stanza, and perform an approved restart/reload. Do not reseed the template over Management UI changes without a reviewed migration.

Controlled authenticated probes have qualified TypeSafe, OpenRouter, and Zen free for the tested credentials. Zen paid must pass a new probe after its funding error is resolved. Local loopback tests prove the host/plugin contract; authenticated probes prove the recorded request's account access, not sustained availability or billing policy.
