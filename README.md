# JEV provider for CLIProxyAPI

Adds native JEV decisions from TypeSafe, OpenRouter, and OpenCode Zen. CLIProxyAPI selects the credential and handles routing, priorities, retries, failover, and cooldowns. This plugin declares only auth-provider, model-provider, and executor capabilities. Each execution sends one request through `host.http.do` using the selected credential context.

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

JEV auth IDs and cooldowns are separate from OpenRouter/Zen chat IDs, even when they reference the same underlying API key. An upstream failure retains its HTTP status with a sanitized message; raw error bodies are not exposed. **RPC schema 6 cannot carry upstream error headers such as `Retry-After`.** The host therefore applies its default status-based retry/cooldown policy. This limitation is accepted in [ADR-0005](../../docs/adr/0005-auth-bound-jev-decision-plugin.md).

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

`test` exercises real RPC handlers; `race` covers concurrent execution/reconfiguration; `transport` builds the actual host and `.so`, then uses temporary auth/config files, fake keys, loopback upstreams, and a guard proxy. It covers all native IDs and aliases, host priorities, disabled/missing/invalid credentials, failover, cooldowns, cancellation, native-body preservation, usage accounting, and unrelated chat execution. It requires local socket access. Set `JEV_TEST_HOST_BINARY` only to a binary built from the current checkout to reuse a host build. Set `GOCACHE` to a writable location when needed.

The root Compose mount is read-only with `create_host_path: false`; build all three plugins before service startup. `dist/` is ignored. A source rebuild does not replace an already loaded native library.

### Local verification record — 2026-09-30

RPC tests, the full Go race suite, `go vet`, and `go mod verify` passed. The Linux amd64 native library built and exports `cliproxy_plugin_init`. The real host loopback passed, including an assertion that its configured proxy was used. Focused host tests for plugin HTTP-status preservation and auth-manager usage publication passed. Root template YAML and Compose mount validation passed. The CLIProxyAPI checkout has no new source changes.

LSP diagnostics were not run because `gopls` is unavailable. Compiler and vet checks ran. Authenticated probes on 2026-09-30 passed for TypeSafe, both OpenRouter credentials, and Zen free, validating `noul`, `choice`, and `score`. Zen paid returned HTTP 402 with `Upstream request failed: Insufficient account funds` and remains unqualified. See the [probe report](../../docs/notes/jev-authenticated-provider-probes.md).

Approved runtime activation on 2026-09-30 passed live checks for all three qualified provider pins and the shared alias. All 133 original public model IDs remain, with five JEV aliases added. Paid Zen is excluded in its runtime auth record; usage statistics retain their previous disabled setting. See the [activation report](../../docs/notes/jev-runtime-activation.md), including the reset-flag recovery and remaining verification limits.

## Activation boundary

Source, template, and mount preparation do not activate the plugin. The persisted config volume is preserved on ordinary startup. Before promotion, obtain runtime approval, back up active config/auths, provision selected records, remove the old `typesafe AI console` compatibility block from active config, add the plugin stanza, and perform an approved restart/reload. Do not reseed the template over Management UI changes without a reviewed migration.

Controlled authenticated probes have qualified TypeSafe, OpenRouter, and Zen free for the tested credentials. Zen paid must pass a new probe after its funding error is resolved. Local loopback tests prove the host/plugin contract; authenticated probes prove the recorded request's account access, not sustained availability or billing policy.
