# Claude Code with native Claude and Codex models on Windows

## Routing contract

The native `claude` command is left unchanged. The `claude-codex` function
points Claude Code at CLIProxyAPI and uses:

| Claude class | Routed model | Provider lane |
| --- | --- | --- |
| Opus | `gpt-5.6-sol` | Codex (native compaction) |
| Sonnet | `claude-grok-4-5` → `grok-4.5` | xAI (client compaction, 500k window) |
| Haiku | `claude-composer-2-5-fast` → `grok-composer-2.5-fast` | xAI (client compaction, 200k window) |
| Subagents / workers | inherit main model, or any gateway model per agent | per chosen model |
| Fable 5 selected through `/model` | native `claude-fable-5` | Claude |

The Sonnet and Haiku classes point at `claude-*` alias IDs that must exist in the
proxy configuration (see the alias block below). Because the alias appears
verbatim in the gateway `/v1/models` listing with the real model's
`max_input_tokens` (500,000 / 200,000), Claude Code sizes its client-side
auto-compaction to the true Grok windows instead of the ~200k unknown-model
fallback.

Subagent and teammate models stay a free per-agent choice. The wrapper
deliberately scrubs `CLAUDE_CODE_SUBAGENT_MODEL`: that variable outranks both an
agent's `model:` frontmatter and per-spawn model selection, so leaving it set
would pin every worker to one model. Without it, a subagent inherits the main
conversation's model unless a `.claude/agents/*.md` definition or an explicit
in-conversation request names another one — any ID the gateway serves works,
including `gpt-5.6-luna`, `gpt-5.6-terra`, the class aliases, `claude-grok-4-5`,
or native `claude-fable-5`. Every worker keeps its own cache and compaction lane
regardless of the model it runs, because lane identity includes the agent header
and the exact requested model.

The Grok lane is deliberately client-compacted: the official Grok CLI
(xai-org/grok-build) has no server-side compaction API and summarizes locally at
85% of the window, so Claude Code's own compaction is the faithful equivalent.
Grok conversation identity follows the official client: the proxy sends a stable
`x-grok-conv-id` header (deterministic per Claude Code session and worker agent)
for every Grok model, which keeps the backend prefix KV-cache warm across turns.
Composer models additionally keep their body `prompt_cache_key` session
isolation. Grok reasoning: Claude Code effort maps to Responses
`reasoning.effort`, clamped to grok-4.5's `low`/`medium`/`high` (an `xhigh`
session becomes `high`); `grok-composer-2.5-fast` has no reasoning levels and the
thinking config is stripped cleanly.

The wrapper sets `CLAUDE_CODE_ALWAYS_ENABLE_EFFORT=1` so Claude Code sends its
selected effort even for custom gateway model IDs. It starts at `xhigh` unless
the caller supplies `--effort`, and `/effort` can change the level during an
interactive session. The model names deliberately have no `(xhigh)` suffix:
suffix-based thinking has higher priority inside CLIProxyAPI and would otherwise
override Claude Code's per-request selection.

Claude Code's **Ultracode** workflow sends `xhigh` model effort plus additional
client-side orchestration. There is no `ultra` API effort value. Use Ultracode
when that workflow is desired, or select `max` with `/effort max` (or
`--effort max`) when maximum model inference effort is desired. CLIProxyAPI
passes both `xhigh` and `max` through to compatible Codex models. The Codex
client's separately named **Ultra** preset is also client-side and is converted
to `max` before a Responses API request. Claude's request does not expose an
Ultracode marker distinct from ordinary `xhigh`, so the proxy must not silently
upgrade every `xhigh` request to `max`.

The wrapper temporarily removes Claude Code's client-side compaction overrides
only for `claude-codex`. This lets the proxy trigger Codex-native compaction at
334,800 logical input tokens and enforce a 372,000-token hard boundary — the
gpt-5.6 family context window from the official Codex CLI catalog, whose client
auto-compacts at 90%. (Older gpt-5.5/5.4 models have a 272,000-token window; if
they are routed through the same instance, keep the smaller bounds instead.)
Normal `claude` retains its native Claude/Fable context and compaction behavior.

When native compaction is enabled, the Anthropic `/v1/models` response reports
a virtual 1,000,000-token input window for models supplied by the Codex
provider. This delays Claude Code's client compaction while the proxy repeatedly
compacts at the real 334,800-token trigger. Native Claude/Fable entries keep
their provider-reported window, so selecting Fable in the same `/model` picker
still uses Claude Code's normal client compaction.

Do not set `CLAUDE_CODE_MAX_CONTEXT_TOKENS` globally for this hybrid setup. A
global value would also shrink the native Fable lane.

## Proxy configuration

Add this block to the canonical YAML configuration:

```yaml
codex:
  native-compaction:
    enabled: true
    trigger-tokens: 334800
    context-window: 372000
    claude-client-context-window: 1000000
    preserve-recent-tokens: 32000
    retained-message-tokens: 64000
    state-ttl: 168h
```

`claude-client-context-window` is picker metadata, not the upstream Codex
limit. Values at or below zero use the conservative 1,000,000-token default.
CLIProxyAPI still triggers native compaction at `trigger-tokens` and enforces
`context-window`. The override applies only to models currently supplied by
the Codex provider; native Claude/Fable and generic OpenAI-compatible providers
are unchanged.

Claude Code may impose an internal maximum or change how it interprets model
metadata in a future release. In that case this setting remains best-effort;
the proxy's native compaction and hard boundary continue to apply, but Claude
Code could still compact earlier than the advertised value.

The proxy first uses Codex's current Responses v2 protocol: it appends
`{"type":"compaction_trigger"}` to an otherwise normal `/responses` request
and advertises `remote_compaction_v2`. It accepts the result only after one
opaque compaction item and `response.completed` are observed. An unsupported v2
request falls back to `/responses/compact`. Transient transport or stream
failures retry v2 once and never permanently downgrade a conversation.

Every compaction request is independently capped to the real context window
minus the configured recent-tail reserve. If a lane is new or reset after
Claude Code has already accumulated more history than one compaction request
can accept, the proxy compacts bounded prefixes in sequence: each opaque result
becomes the root of the next prefix until the rewritten request is safe. A
deterministic `context_length_exceeded` response is replanned with a smaller
prefix instead of retrying the same oversized body.

The proxy retains a recent exact tail outside compaction so the user's active
turn and tool-call pairs are not split. After compaction, later Claude Code
requests are rewritten as the saved replacement history plus only the exact
post-boundary delta. This produces one expected cache-root transition at a
successful compaction, then restores append-only cache continuity.

Claude-originated Codex turns also reuse the most recent valid encrypted
reasoning item. Replay is inserted before compaction is planned, so it is
either included once in the summarized prefix or kept once in the exact tail.
The durable source boundary is still calculated only from client-owned history;
the transient replay item never becomes a source hash or a second append.
Rejected encrypted reasoning is tombstoned per Codex credential and session,
without deleting the auth-independent replay cache used for failover. Companion
tool calls remain available so recovery cannot orphan a tool output.

Claude Code's session header identifies the conversation, while its agent
header identifies each worker. The proxy scopes Codex prompt-cache identity,
reasoning replay, and native-compaction state by both values. Interleaved Opus
workers can therefore use different tool envelopes without resetting or
recompacting one another's history. Headerless requests keep a distinct main
conversation lane for compatibility with older clients.

If native compaction has a transient failure below the configured hard
boundary, the current
valid lane is sent and a warning is logged. If Codex rejects encrypted state,
the proxy atomically retires the suspect summary, rebuilds from authoritative
Claude history, suppresses only implicated reasoning, preserves tool pairs, and
retries once. A summary rejected by the following generation is treated as the
first suspect so unrelated recent reasoning is not blacklisted. At or above the
hard boundary the request fails explicitly instead of silently discarding
history. Compaction state is committed only after a validated terminal response
and an atomic durable write. The agent-aware state files live under
`<auth-dir>\state\codex-native-compaction\claude-code-compaction-v2-*.json`;
they are versioned, checksum-verified, and expire with `state-ttl`. Authenticated
v1 files are retired automatically because their session-wide keys cannot be
safely reused by agent-scoped lanes. The state
directory and files receive a user-only ACL on Windows (and `0700`/`0600`
permissions on Unix-like systems); periodic sweeps remove expired valid lane
files while leaving unrelated or corrupt evidence untouched. Do not delete
active files during a Claude Code conversation: they preserve the exact
post-compaction cache root across proxy restarts.

## Authentication and the `/model` picker

Codex, Claude, and xAI OAuth must all be active in the same CLIProxyAPI
instance. Run the interactive logins from the fork binary when a provider has no
usable auth file:

```powershell
.\cli-proxy-api.exe --config .\config.yaml --codex-login
.\cli-proxy-api.exe --config .\config.yaml --claude-login
.\cli-proxy-api.exe --config .\config.yaml --xai-login
```

A missing xAI auth surfaces as `503 auth_unavailable` on the Sonnet and Haiku
classes while Opus (Codex) and Fable (Claude) continue to work.

Background-task caveat: Claude Code may route conversation summarization and
titling through the Haiku class. If a summarization of a very long Codex-lane
conversation (whose logical transcript can exceed 200k tokens under the virtual
window) is ever sent to `claude-composer-2-5-fast`, it will overflow composer's
200k window and that compaction attempt fails. If this appears in practice,
either point the Haiku class back at a Codex model or lower
`codex.native-compaction.claude-client-context-window` toward 500000 so client
compaction fires earlier.

Claude OAuth is what allows native Fable selected inside `/model` to remain on
Claude. A stale Claude auth file produces `503 auth_unavailable` even when the
three default Claude classes route successfully to Codex.

Optional extra picker labels can be added without replacing the native class
mapping:

```yaml
oauth-model-alias:
  codex:
    - name: gpt-5.6-sol
      alias: claude-codex-opus
      fork: true
    - name: gpt-5.6-terra
      alias: claude-codex-sonnet
      fork: true
    - name: gpt-5.6-luna
      alias: claude-codex-haiku
      fork: true
  claude:
    - name: claude-fable-5
      alias: claude-native-fable
      fork: true
  xai:
    - name: grok-4.5
      alias: claude-grok-4-5
      fork: true
    - name: grok-composer-2.5-fast
      alias: claude-composer-2-5-fast
      fork: true
```

Leave `force-mapping` disabled. The class environment variables remain the
primary Opus/Sonnet/Haiku mapping.

Install the PowerShell function idempotently from the binary. The local proxy
key is read from `config.yaml` and is not printed:

```powershell
.\cli-proxy-api.exe --config .\config.yaml --install-claude-code-aliases
. $PROFILE.CurrentUserAllHosts
```

Use `--alias-dry-run` to preview whether the selected profile would change.
The installer never replaces the native `claude` command.

## Observability

Every upstream attempt emits one metadata-only `request_event` line with
provider, resolved model, operation, input/output/cache tokens, estimated cost,
failure state, and latency.

Inference failures that happen before an executor can publish usage (for
example `auth_unavailable` or request validation failures) produce one
unpriced synthetic event. A per-request publication marker prevents a normal
executor record from being counted a second time.

- A reported cache miss is red. A low-reuse request, where more than half of
  normalized input was not served from cache, is also red without being
  reclassified as a strict miss.
- A compaction is magenta unless the same attempt is a cache miss, in which case
  red takes precedence.
- Redirected output and file logs never receive ANSI escape sequences.

The fixed TUI row shows request count, input/output tokens, strict cache misses,
low-reuse requests, successful compactions, compaction-lane resets, and
estimated cost. Resets are metadata-only diagnostics with hashed lane, agent,
and tool-envelope identifiers; they do not expose prompts or raw IDs and do not
inflate request/token/cost totals. Failed compaction attempts remain in the
event log and are exposed separately as `compaction_attempts` by:

```text
GET /v0/management/observability
```

When file logging is disabled, a remote TUI still populates its Logs tab through
the endpoint's cursor-based metadata-only event feed. A bounded-buffer gap or
server restart is shown explicitly instead of silently dropping events. This
keeps colored request tracking available without persisting prompt-bearing
error logs. The cost row shows `partial` when any request is unpriced and `—`
when no priced request exists; values are estimates, not subscription charges.
Fable cache writes use the provider's reported TTL breakdown: 5-minute writes
are estimated at `$12.50/MTok`, 1-hour writes at `$20/MTok`, and an
unclassified remainder is conservatively estimated at the 1-hour rate.

The Logs tab defaults to a fixed-header request monitor with these columns:
provider, model, effort, input, output, cache read, cache write, cache-read
percentage, input cost, output cost, and cache cost. Press `v` to switch between
the request table and ordinary raw server logs. Cache misses and low-reuse rows
remain red; compaction rows remain magenta.

OpenAI Responses usage includes cached reads but the Codex subscription endpoint
currently reports `cache_write_tokens: 0` even while the next request reuses the
new prefix. For Codex rows, the proxy therefore displays the uncached portion as
a compatibility estimate for cache-eligible prompts (1,024+ input tokens) and
prefixes it with `~`; metadata logs also emit
`cache_write_estimated=true`. With
`codex-claude-estimate-cache-write-usage: true`, the same estimate is returned
to Claude Code as `cache_creation_input_tokens` so its cache-creation counter is
useful. This is an explicit compatibility mode because Anthropic's wire schema
cannot label that counter as estimated. It is not provider-confirmed and is
never used for cache-write pricing: the provider's reported zero remains
authoritative for cost, and the estimated portion remains normal OpenAI input.
Provider-confirmed GPT-5.6 writes use OpenAI's documented 1.25x input rate.
Native Claude/Fable cache-write values stay provider-reported.

For bounded request diagnostics, use:

```yaml
request-log: true
request-log-success-summary: true
request-log-summary-rotation-hours: 5
request-log-summary-max-files: 48
error-logs-max-files: 50
logs-max-total-size-mb: 1024
codex-claude-estimate-cache-write-usage: true
```

Successful requests then append one masked, body-free JSON object to a
`request-summary-*.log` file. Files use fixed five-hour UTC windows, and the
oldest summary windows are removed after the configured limit. Failed requests,
including streamed failures detected after downstream HTTP 200 headers, retain
a full `error-*.log` diagnostic. Those error files can include prompts, source,
tool inputs/results, and response content; treat them as sensitive. Temporary
streaming parts are removed after the final summary or error log is committed.

Run `-tui -standalone` for an all-in-one foreground instance. When attaching a
TUI to the background proxy, configure a loopback-only management secret. This
fork lets the pure TUI client inherit `MANAGEMENT_PASSWORD` when `-password` is
omitted, so the secret does not need to appear in process arguments or logs.
