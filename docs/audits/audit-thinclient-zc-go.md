# Audit: Go thin-client rewrite (commit 2837ec6) — zc side of the seam

**Date**: 2026-07-16 · **Auditor**: ZeptoCode Architect (deep-dive subagent + live tmux reproduction)
**Verified live**: `pixi run build` ✅ clean · `go vet` ✅ clean · `go test ./...` ✅ (only list tests exist) · ui-server hello handshake + `conversations_list` work end-to-end.

---

## 1. Startup flow — root-cause diagnosis of "doesn't get past the agent picker"

Actual trace of `zc` with no `--agent` (main.go:330–519, 1399–1403, 352–398, 402–418):

```
connectCmd → listAgentsHeadless() → picker overlay (phasePicking)
  → enter on agent → startupConvPicker(it.id)      [spawns temp ui-server #1, lists convs, kills it]
  → conversation picker (overlayMgmt)
  → enter on conversation → onSelect: m.startOpts.ConversationID = it.id
       → startRuntimeCmd(agentID)                   [agentID parameter is DEAD]
           → opts := m.startOpts                    [opts.Agent == "" — never updated!]
           → ConnectStdio(opts) → "stdio: Agent is required" (stdio.go:55)
  → runtimeReadyMsg{err} → "startup failed: stdio: Agent is required"
```

**BROKEN (primary root cause, reproduced live in tmux)** — main.go:402–418: `startRuntimeCmd(agent string)` **never uses its `agent` parameter**. It builds `opts := m.startOpts`, and `m.startOpts.Agent` is never assigned from the picker selection (only `ConversationID` is, at main.go:393). `ConnectStdio` rejects `Agent == ""` instantly. **Every no-`--agent` launch dead-ends with "startup failed: stdio: Agent is required", 100% deterministic.** This is the exact `SetConversation`-before-`StartRuntime` bug class from 1ebe567, reintroduced.

**BROKEN (secondary, masked)** — main.go:439: `listAgentsHeadless` runs `letta agents list --json`. **Verified live: there is no `--json` flag** (output is JSON-only by default) — the command exits 1. Even without the flag, output is `{"items": [...]}`, not the bare array `json.Unmarshal` expects — the primary path can *never* work; the picker is fed exclusively by the `listAgentsFromBackend()` fallback (raw reads of `~/.letta/lc-local-backend/agents/*.json`). Fallback includes hidden/scratch agents, unsorted (verified live: wall of "Letta Code" junk rows).

**Perceived-hang contributor** — after picking an agent, `startupConvPicker` spawns a full bun+harness ui-server just to list conversations, with no progress feedback; cold start takes many seconds.

**SPEC-VIOLATION (G3/§9)** — startup pickers were specced as `agents_list`/`conversations_list` frame queries. The agent picker instead shells out + reads backend files (main.go:436–480) — exactly the domain logic the rewrite was supposed to delete. Underlying design gap: the ui-server **requires `--agent` at spawn**, so `agents_list` can't serve the startup picker (chicken-and-egg). SPEC.md never defines pre-spawn agent listing → needs a spec decision (lobby mode / agent-optional spawn / sanctioned headless listing).

**REGRESSION (conversation litter, verified live)** — the ui-server creates a new conversation on *every* spawn without `--conversation`; the startup flow spawns 2–3 connections per launch → 2 abandoned empty conversations per launch (observed local-conv-276..286 accumulating during the audit). Resurrects the exact litter bug 1ebe567 + /tidy fixed.

**BROKEN (process leak, `--agent` path, verified live)** — main.go:500–519: with `--agent`, `connectCmd` connects (child #1, `m.cli = msg.cli`), then `startupConvPicker`/`startRuntimeCmd` spawn children #2/#3 and reassign `m.cli` — **child #1 is never Closed and runs until zc exits** (two live `bun ui-server` processes observed for one zc session).

**SMELL (UX trap)** — main.go:797–805: in startup phases, esc closes the overlay with no way to reopen the picker → stuck at spinner until ctrl+c.

---

## 2. Seam mismatch table (Go ↔ ui-server.ts, both directions)

Go outbound → server inbound:

| Go sends (stdio.go) | ui-server case | Verdict |
|---|---|---|
| `hello` | ✅ handled | **OK**, but pending-channel registered *after* send → hello race (§3) |
| `input_submit {content}` | ✅ | OK |
| `control_request {subtype:"interrupt"}` | ✅ exists but dead server-side (F3) | **UNREACHABLE from Go anyway** — loopStatus cluster §4 |
| `control_response` (approval) | ✅ shape matches | OK; but Go drops `decision.Message` (main.go:858 custom deny message never sent — SendApprovalResponse hardcodes "Denied by user") and **never sends `selected_permission_suggestion_ids`** (server also hardcodes `permission_suggestions: []`) → numbered-suggestion persistence dead on both sides. **REGRESSION** |
| `execute_command {command_id:"model", args}` | ✅ case exists, but Go builds input **without leading slash**; registry keys are `"/model"` → always "Unknown command" | **BROKEN** (currently unreachable — see §7, two bugs cancel out) |
| `change_device_state {mode}` | ✅ case, but mode echoed, not enforced server-side | **DRIFT** — mode pill is cosmetic |
| `change_device_state {cwd}` | ✅ `process.chdir` | OK |
| `agents_list {limit:200}` | ✅ | shape trap: server returns `{items}`-wrapped full AgentStates (see TS audit F5) |
| `conversations_list {agent_id,limit}` | ✅ | OK (verified live) |
| `conversation_messages_list` | ✅ | OK (Delta-compatible) |
| `models_list` | ✅ | OK shape; Go never uses it (dead method) |
| `update_model {model_id\|model_handle}` | ⚠️ server ignores `model_handle` yet replies success | **BROKEN** — `/model provider/handle` silently no-ops |
| `conversation_fork` | ⚠️ server "fork" = createConversation (empty) | **BROKEN semantics** — /fork discards history |
| `conversation_update` | ✅ | OK |
| `agent_update {updates}` | ✅ | OK |
| `agent_retrieve` | ⚠️ Go parses `agent.model`; real field is `agent.llm_config.model` | **DRIFT** — masked by device_status.model |
| `mod_panels_query` | ✅ | Go never calls it (dead both ways) |

Server outbound → Go Decode/handleFrame:

| Server emits | Go handling | Verdict |
|---|---|---|
| `hello_response`, `device_status`, `turn_start`, `turn_end`, `transcript_update`, `control_request`, `command_result`, `bash_output`, `error` | decoded + handled | OK |
| `transcript_sync {entries}` — server means "this turn's buffers, at end_turn" | Go treats it as **authoritative full transcript**: `resetEntries()` + rebuild (main.go:1595–1621) | **BROKEN (critical)** — after every completed turn the entire rendered transcript (replayed history + prior turns + tool cards) is **wiped and replaced by the current turn's flat text**. Both sides internally consistent with different meanings of "sync" — classic two-model seam bug |
| `turn_cancelled` | no Decode case → silently dropped | **DRIFT**; also server always follows with `turn_end{stop_reason:"end_turn"}` so Go's "cancelled" branch (main.go:1588) is dead |
| `control_response` (interrupt ack) | no top-level request_id → dropped | harmless noise |
| `settings_updated`, `mods_updated` | not decoded → dropped | OK today, spec'd for §5.4 |
| `command_result.success` | server hardcodes `success:true` on input_submit path | **DRIFT** — failures render as info |
| legacy v2 frames (`stream_delta`, `update_loop_status`, `update_device_status`, `update_queue`, `update_subagent_state`, …) | Go still decodes them; server never sends them | dead weight with live consequences (§4) |

**Seam-interaction landmine**: Go leaves input enabled during turns (no working `turnActive`). A user submit while a `control_request` is pending triggers the server's approval busy-loop (TS audit F4) → **approval deadlock at 100% CPU**. Server-side bug; Go's missing input gating is the trigger.

---

## 3. StdioClient (internal/client/stdio.go)

- **BROKEN (config)** — line 62: default `UIServerPath` is a **hardcoded dev path** `$HOME/projects/letta-code/src/cli/subcommands/ui-server.ts`; `UIBin` defaults to `bun` from PATH. No flag, no env var, no config anywhere in main.go. Works only on this machine with the fork at that exact path on the right branch.
- **RACE (hello)** — lines 102–122: `sendRaw(hello)` happens **before** `request()` registers the pending channel. A fast `hello_response` misses the pending map, lands on `Frames`, is discarded (main.go:1545) → handshake burns the full 30s then errors. Intermittent startup failure by design.
- **BROKEN (frame drop under load)** — lines 163–167: push delivery is `select { case c.Frames <- frame: default: }` — when the 256 buffer fills, **transcript frames are silently discarded**. The old WS client never dropped. Corrupted transcripts under exactly the load batch-drain was built for.
- **SMELL (death handling)** — on child death, `close(c.Frames)` fires but pending request channels are never failed → in-flight `request()`s wait out their full timeouts.
- **SMELL** — `Close()` doesn't wait for readLoop; `GetOpts`/`SetConversation`/`SetMode` mutate `c.opts` unsynchronized.
- **BROKEN (flag lies)** — `StdioOptions.Mode` stored, never transmitted (no `--mode` on the shim, hello carries no mode) → `zc --mode unrestricted` is a silent no-op. `--port` flag equally dead.
- Correlation-by-request_id works for all typed responses; but the server's unknown-frame `error` reply carries no request_id → future unknown requests hang for the full timeout instead of failing fast.

---

## 4. Frame handling completeness + the loopStatus cluster

**BROKEN (cluster)** — `m.loopStatus` is never written by any ui-server frame (server has no loop-status frame). It stays `WAITING_ON_INPUT` forever → `turnActive()` always false. Cascade:
1. **Esc cannot interrupt a turn** — main.go:906 gate never passes; `SendInterrupt` unreachable (and the server side is dead code too — double-broken).
2. **Ctrl+c double-press arming never triggers mid-turn** (main.go:890) — first ctrl+c quits instantly.
3. **Spinner never spins during turns** — `turn_start` sets `m.spinning` but no tick is scheduled (framesMsg gate at main.go:583 requires `turnActive()`).
4. **Statusline always says "ready"**, even mid-stream (observed live).
5. Input stays enabled mid-turn → feeds the server-side approval livelock.

Other completeness gaps:
- `m.queue`, `m.subagents` never populated → `activityLines()` dead. **REGRESSION** (queue/subagent visibility).
- `m.supportedCmds`/`m.modCmds` never populated — server's `command_catalog` handler exists but **Go never sends the request** → palette/completions list only the 12 zc commands. **REGRESSION**.
- `usage_statistics` accumulates into fields nothing displays (dead state).
- `pending_control_requests` approval recovery: gone on both sides. **REGRESSION** (previously fixed, audit-letta-gaps A1).
- `DeviceStatusFlat.Tools` parsed then explicitly ignored (main.go:1576–1579 no-op).

---

## 5. Spec deviations — domain logic still in Go (§3.4 "Nothing else")

| Site | What it does | Verdict |
|---|---|---|
| main.go:436 `listAgentsHeadless` | shells `letta agents list --json` | SPEC-VIOLATION + broken flag |
| main.go:455 `listAgentsFromBackend` | reads `~/.letta/lc-local-backend/agents/*.json` | SPEC-VIOLATION (raw backend poking) |
| main.go:228 `readZcConfig` | reads `~/.letta/zc.json` | tolerated per G8 (client rendering pref) |
| main.go:246 `readReasoningTabSetting` | reads `~/.letta/settings.json` directly | SPEC-VIOLATION (spec: `settings_read`) |
| main.go:1261 `/export` | walks entries, writes a file client-side | SPEC-VIOLATION (spec: `agent_export` frame; server handler exists, unused) |
| main.go:1242 `/cd` | `~`-expansion + relative resolution client-side | SMELL (borderline terminal-truth) |
| completion.go:83–121 | @-path completion via local `os.ReadDir` against serverCWD | SPEC-VIOLATION (spec §5.19: `search_files`/`read_file`; handlers exist, unused) |
| overlay.go:125–233 | jobs manifests + broker HTTP reads | dead code (unreachable) but compiled in; G5 says /jobs = mod command + panel |

---

## 6. Regressions vs pre-rewrite (5b38a42)

Slash routing today: anything not in the 12 client commands → `SendMessage("/cmd …")` → server `executeCommand` → native registry. Wiring exists, but the native handlers are mostly App.tsx placebos (see TS audit F6), and all zc-native interaction UIs were deleted:

- **/jobs — gone-and-broken.** Mod commands are NOT in the builtin registry; ui-server never consults the mod adapter's command registry → "Unknown command". The jobs *panel* is implemented on both ends but Go never sends `mod_panels_query`. Help text still advertises "/jobs" (keymap.go:40). Same fate for /audit and every ZeptoCode mod command. G5 explicitly required these.
- **/usage, /subagents, /context, /bg — gone-and-broken-ish**: server data handlers exist (`session_stats`, `subagent_defs_list`, `context_window_overview`, device bg) but Go never calls them; typed as text they hit native registry placebos (observed live: `/usage` → "Fetching usage statistics..." then nothing, and the corresponding stats frames are permanently zero server-side anyway).
- **/secret, /skills, /connect, /memory, /memory-repository, /mods, /hooks, /profiles, /crons, /tidy, /search, /reflect, /system, /personality, /pin, /unpin, /memfs, /compaction, /experiments, /sleeptime, /description, /recompile, /reasoning-tab, /statusline, /feedback, /skill-creator, /mcp** — all deleted client-side; all now naked `input_submit` → placebo text. Interactive flows (masked secret entry, provider forms, memory pager, cron confirm, tidy confirm) **lost their entire interaction model** — nothing produces `overlay_state` (Go's handler at main.go:1646 is wired to a frame with no producer). **Phase-3 "management works via frames" was not delivered: zero Go call sites use any of the Stage 3–5 data frames.**
- **AskUserQuestion form — gone-and-broken**: server excludes the tool (headless-copied `exclude`) → question.go unreachable. Was a flagship feature.
- **Approval permission suggestions** (numbered persist-rule buttons) — dead both sides.
- **Approval recovery on reconnect** — dead both sides.
- **Model picker & reasoning-variant cycling — gone-and-broken**: `modelsMsg`/`overlayModels` have no producer; `openModelPicker` sends "/model" as text; `cycleReasoningVariant` is a stub (main.go:1083).
- **Periodic idle sync (commit 20961ce) — NOT IMPLEMENTED AT ALL.** No ticker, no re-query, no diff. The commit only edited the spec. Background-job message visibility (G6, the whole point of the jobs rework) is unhandled.
- **Kept ✅**: version drift warning, batch-draining (undermined by the stdio drop bug), empty-conversation honesty, KeyReleaseMsg swallowing, esc-priority chain, content-change-gated completion refresh.
- **Reconnect/auto-respawn — kept but now harmful** (§8: fires spuriously on every switch).
- **Input history** — in-memory only (spec wanted `~/.letta/zc-history.jsonl`; never existed — unmet spec item, not a regression).
- **pixi tasks — broken**: `spike` references deleted `cmd/spike`; `smoke` runs deleted tests ("[no test files]" — silently proves nothing). stdio-smoke hardcodes the probe agent id (was env-driven).
- **Pager modal — dead**: `pagerRequest` has no producer/consumer; pager.go unreachable; entryDoc/entryDiff kinds have no producers.

---

## 7. The 12 kept client commands vs spec §4.1

Spec said only /help and /quit stay. Actual: 12 kept.

| Cmd | Spec disposition | Actual | Verdict |
|---|---|---|---|
| /agents | overlay via `agents_list` | ✅ uses the frame (in-chat) | fine per G1; pinned-star feature stubbed to nothing (main.go:1102) |
| /conversations | frames | ✅ `conversations_list` | fine |
| /new | `conversation_create` + switch | kills child + respawns (new conv via spawn side-effect) | works, heavyweight; `conversation_create` handler unused |
| /mode | `change_device_state` | sends frame; server doesn't enforce | cosmetic (server bug) |
| /model | `models_list` overlay + `update_model` | no-arg: "/model" as text (placebo); with-arg: `update_model` (handle variant silently ignored); **blocking IO in Update** | BROKEN in parts |
| /fork | `conversation_fork` | calls it; server fork isn't a fork + kill/respawn | BROKEN semantics |
| /title | `conversation_update` | ✅ frame — **blocking IO in Update** (up to 10s freeze) | works, freezes |
| /rename | `agent_update` | ✅ frame — blocking IO in Update | same |
| /cd | `change_device_state{cwd}` | ✅ + client-side path resolution | fine-ish |
| /export | `agent_export` | ❌ client-side file write | SPEC-VIOLATION |
| /help, /quit | stay | ✅ | fine |

Landmines:
- `modelSwitchedMsg` success path dereferences `msg.resp.ModelHandle` but `switchModel` never sets `resp` → **guaranteed nil-pointer panic** if `overlayModels` is ever re-wired (main.go:619–627 + 1073–1079).
- `dispatchSlashCommand` (main.go:1485) diverges from spec Appendix B: *known* non-client commands route via `execute_command` — which is broken at the seam (missing leading slash, §2). Only saved by `knownCommands()` being perpetually empty (catalog never fetched). **Two bugs cancelling each other.**

---

## 8. Bubbletea correctness

- **BROKEN (race + spurious reconnect)** — `switchConversation`/`switchAgent`/fork/startRuntimeCmd all `old.Close()` inside a tea.Cmd → readLoop dies → `Frames` closes → pending `waitForFrame` returns `disconnectedMsg` → Update appends "connection lost — reconnecting…" **and spawns `m.reconnect()`** with the OLD opts, racing the switch's new client for `m.cli`. Every switch produces an error banner, a zombie process, and a coin-flip over which client wins. No `m.switching` guard on `disconnectedMsg` (main.go:660–670).
- **RACE (memory model)** — tea.Cmd closures assign `m.cli = cli` (main.go:410, 1132, 1212, 1443) while Update/View read it. `-race` would flag it.
- **IO in Update** — `/title`, `/rename`, `/model <arg>` run 10s round-trips synchronously inside `handleKey`. Known bug class (old /jobs HTTP bug).
- KeyReleaseMsg swallowing ✅, batch-drain ✅ (undermined by stdio drops), completion refresh gating ✅.
- `statusline()` reads `m.cli.Runtime.*` without a nil guard — safe only via phase ordering; latent panic (SMELL).
- `connectDoneMsg` with agents assigns `m.cli = msg.cli` (nil) — currently ordered safely, fragile (SMELL).

---

## 9. Build/test truth

- `pixi run build`, `pixi run vet`: clean (verified).
- `go test ./...`: only `internal/ui/list` + completion tests; zero coverage of stdio client, protocol decode, startup flow.
- `cmd/stdio-smoke`: compiles; proves hello + one text turn + turn_end against a hardcoded agent id. Does NOT exercise pickers, transcript_sync semantics, approvals, interrupt, switches, or any list/CRUD frame. The "8 frames end-to-end" commit claim is real but narrow.
- `pixi run spike`: broken (deleted dir). `pixi run smoke`: silently vacuous.

---

## 10. Survived strengths (don't regress in the fix pass)

Batch-drain, version-drift warning, empty-conversation honesty, KeyReleaseMsg swallowing, esc-priority chain.
