# Audit: `letta ui-server` (fork `zc-ui-server`, commit 89b793a9) — TS side of the zc protocol

**Date**: 2026-07-16 · **Auditor**: ZeptoCode Architect (deep-dive subagent, source-verified against native modules)
**Scope**: `src/zc/ui-server.ts` (1,764 lines), `src/cli/subcommands/ui-server.ts` (69 lines), `src/cli/subcommands/router.ts` (+4). Verified against native modules in the same tree and `docs/protocol/SPEC.md` / `GO-THIN-CLIENT-SPEC.md`.

**Executive verdict**: Stage 0 (hello handshake, stream-out of a plain text turn, boot with TUI mod capabilities) is real. Stages 1–5 are largely **façade**: handlers exist for ~34 frame types, but the file has **26 TypeScript errors (`bun run typecheck`; upstream is clean — all 26 are in the two new files)**, several of which prove dead or crashing code on core paths. **Any turn that calls any tool crashes the server** (bad module path + wrong `executeApprovalBatch` signature), **esc-interrupt is provably unreachable** (TS narrows the abort controller to `never` at the call site), and **any extra frame arriving during an approval wait livelocks the process at 100% CPU**. The commit's "Verified:" list is exactly the happy paths that avoid tools, approvals, panels, and interrupts.

---

## 1. Fatal defects (unusable-in-practice class)

| # | Defect | Evidence | Consequence |
|---|---|---|---|
| F1 | **Any tool call crashes the server.** `await import("@/headless/tool-output")` — module does not exist (real location: `src/headless-tool-events.ts`). TS2307 at L604. Unwrapped by try/catch inside the `requires_approval` branch → exception propagates out of `processTurn` → main loop → process exit 1. | ui-server.ts:604; `ls src/headless*` | Every client-side tool (Read, Bash, Edit — all of them arrive as `requires_approval`) kills the session. Even fully auto-approved tools. |
| F2 | **`executeApprovalBatch` called with an invented 6-arg signature.** Real: `(decisions, onChunk?: (chunk)=>void, options?)` (approval-execution.ts:375). Impl passes `agent.id` (string) as `onChunk` → `if (onChunk) onChunk({...})` at approval-execution.ts:214 → **TypeError: not a function**. Return is `ApprovalResult[]`; impl reads `.toolReturnMessages` / `.updatedAgent` (both `undefined`) → `currentInput = undefined` for the continuation. Native continuation wraps results as `{type:"approval", approvals: executedResults, otid}` (headless.ts:5040-5052). TS2554/TS2339 at L611/617/618. | ui-server.ts:607-618 vs headless.ts:5037-5052 | Even if F1 were fixed, approval continuation is triple-broken: crash in callback, no results extraction, wrong continuation input shape. |
| F3 | **Interrupt is dead code — proven by the type checker.** The main loop only reads stdin between turns; `processTurn` sets `currentAbortController = null` before returning, so at the `control_request{interrupt}` case the controller is always null — TS2339 "Property 'abort' does not exist on type **'never'**" at L719. Native headless bidirectional solved this with a **fast-path interrupt check inside `rl.on("line")`** ("Without this, a runaway thinking turn never sees the interrupt", headless.ts:3998-4050) — not copied. | ui-server.ts:715-732, headless.ts:3998 | Esc-interrupt from the Go client can never cancel a running turn. It's processed *after* the turn finishes, as a no-op. |
| F4 | **Livelock on any non-matching frame during an approval wait.** `requestPermission`'s loop: `getNextLine()` → not the matching `control_response` → `lineQueue.unshift(line)` → loop → `getNextLine()` shifts the *same line* → unshift → … a microtask-only spin that **starves the I/O event loop**, so the real `control_response` can never even be enqueued. Triggers: a `mod_panels_query` poll, an interrupt, a queued second input. | ui-server.ts:409-430 | Approval modal + statusline polling = guaranteed hang, 100% CPU. |
| F5 | **`agents_list` response shape**: local backend `listAgents` returns **`{ items: AgentState[] }`** (local-store.ts:1224) while `listConversations` returns a **plain array** (local-store.ts:1489). Impl passes both through raw (`agents: {items:[...]}`). Also each item is a **full projected AgentState (system prompt + memory included)** — a 200-agent response is one multi-hundred-KB..MB JSON line (Go `bufio.Scanner` cap hazard). | ui-server.ts:1040-1058 | A picker built on `agents_list` either decodes an empty/mismatched list or kills its scanner on an oversized line. |
| F6 | **Slash-command routing is a placebo for ~54 of 64 commands.** `executeCommand` (registry.ts:673) dispatches to registry handlers, but the registry handlers for /agents /model /memory /skills /clear /compact /new /fork /rename /search /system /crons… are stubs returning strings like `"Opening agent browser..."` — real behavior lives in App.tsx ("Handled specially in App.tsx" ×54). The seam plan (SPEC §2.3, §5.5 overlays) required extracting those; nothing was extracted, no `overlay_state` frames exist. Bonus: impl ignores `result.success`/`notFound` and reports `success: true` for unknown commands. | ui-server.ts:690-708, 781-802; registry.ts:27-651 | `/anything` from zc returns a decorative string. No overlays, no state changes. Also **mod commands (/audit, /jobs, /track-agent) are not routed at all** — `executeCommand` knows only the builtin registry; the mod adapter's command registry is never consulted → "Unknown command". |

---

## 2. Per-frame verdict table

Legend: **OK** = native-faithful · **DRIFT** = works but diverges from native/spec · **STUB** = returns something but not the real feature · **BROKEN** = errors/crashes/wrong data.

| Frame (in) | Native module it should use | What impl actually does | Verdict | Line |
|---|---|---|---|---|
| `hello` | — (new) | Handshake OK; responds after full boot (slow init delays it; no timeout guidance) | OK | 332-376 |
| `input_submit` (message) | shared submit path + accumulator | `sendMessageStream` + `drainStreamWithResume` + `createBuffers` — genuinely the shared path | OK-ish (see turn loop §4) | 434-633 |
| `input_submit` (bash `!`) | native bash handling (`use-bash-handlers` extraction per §2.3) | Hand-rolled `exec()`, 1 MB buffer, output → `bash_output` frame + `<bash-output>` prefix on next msg. No `shell` transcript entry, no `<bash-input>`, no alias expansion, no cwd persistence, not in transcript_sync | DRIFT | 659-688 |
| `input_submit` (slash `/`) | command registry **+ App.tsx extractions + overlays** | `executeCommand` only → placebo strings for ~54/64 cmds; `success` ignored; mod/custom commands unreachable | STUB/BROKEN (F6) | 690-708 |
| `control_request{interrupt}` | headless fast-path (headless.ts:3998) | Dead code (controller provably null); no fast path in `rl.on("line")` | BROKEN (F3) | 715-732 |
| `control_response` (approval) | headless requestPermission loop | Shape mirrors headless, but unshift-loop livelocks on any interleaved frame | BROKEN (F4) | 409-430 |
| `mod_panels_query` | `renderModPanelLines` + full `ModPanelRenderContext` (InputRich.tsx:502-514; types.ts:470-483) | Calls `p.render({width, columns: <number>, row: 0})` — `row`/`columns` must be **functions**, and the full ModContext (agent/model/chalk/…) is absent → any real panel render throws, is caught, skipped → empty panels | BROKEN | 734-777 |
| `execute_command` | registry + overlays | Same placebo problem as slash routing; `req.command_id` may be undefined (TS2345 L786) | STUB | 781-802 |
| `settings_read` | settings-manager effective view + revision (SPEC §5.7) | `getSettings()` + name-heuristic redaction (top-level keys containing key/secret/token); no revision; response named `settings_response` not `settings_read_response` | DRIFT | 806-832 |
| `settings_patch` | settings-manager as sole writer, scope routing, RFC-7386, revision CAS | Raw `writeFileSync` shallow-merge of **global settings.json only** — recreates the exact external-writer cache race the design was meant to kill (mitigated by `reset()` after); nested keys clobbered; no scope routing; no revision | DRIFT (design violation) | 834-862 |
| `hooks_list` | `loadHooks()` | Correct call | OK | 866-884 |
| `hooks_update` | `saveHooksToLocation(hooks, location: "user"\|"project"…)` | Passes a **file path** as the `SaveLocation` enum (TS2345 L891) → falls through switch, silent no-op | BROKEN | 886-905 |
| `search_messages` | `searchMessagesForBackend` (backend-aware, message-search.ts) | Imports `@/backend/api/search` — the **API-backend HTTP endpoint** → fails on the local backend, returns error+empty | BROKEN (on local backend) | 909-934 |
| `skills_list` | `discoverSkills` | Correct module; project path + agentId passed; no per-source options (minor) | OK | 938-960 |
| `context_window_overview` | native context stats | Reads `sessionStats` which is **never fed anywhere in the file** → always zeros | STUB | 964-990 |
| `session_stats` | SessionStats accumulated per turn (headless feeds it) | Same: `new SessionStats()` at L272, never updated → all zeros | STUB | 994-1011 |
| `conversations_list` | `backend.listConversations` | Correct; limit 200; note plain-array return (vs `{items}` for agents) | OK | 1015-1036 |
| `agents_list` | `backend.listAgents` | `{items}` wrapper passed through; full AgentStates (huge frames) | BROKEN-for-client (F5) | 1040-1058 |
| `models_list` | structured catalog (protocol_v2 `list_models` semantics: ids, labels, availability) | `formatAvailableModels()` — a **preformatted human string**. No ids → a model *picker* cannot be built from this | STUB | 1062-1080 |
| `update_model` | `getModelUpdateArgs(modelId)` + conversation-vs-agent scope | Calls with 2 args (TS2554 L1090; 2nd ignored); `model_handle` accepted but ignored; `scope` echoed but only agent-level update performed; undefined updateArgs → `updateAgent(agent.id, undefined)` | DRIFT/BROKEN edges | 1084-1108 |
| `change_device_state` | `permissionModeState` plumbing into `sendMessageStream` opts + persisted mode | `chdir` works; **mode is echoed and discarded** — never stored, never passed to classify/execute/mod-context → mode cycling is a no-op on actual approvals | BROKEN (silent) | 1112-1127 |
| `conversation_messages_list` | `backend.listConversationMessages` | Correct call (no pagination body) | OK | 1131-1151 |
| `conversation_create` | `backend.createConversation` | OK but declared `title` param ignored | DRIFT (minor) | 1155-1174 |
| `start_reflection` | `launchReflectionSubagent` | Real call, plausible options (verified fields exist); emits nonstandard `reflection_started` + `reflection_result` | OK-ish | 1178-1214 |
| `subagent_defs_list` | subagent **definitions** (SPEC §5.11) | `getSubagents()` = live subagent **lifecycle state**, not defs | DRIFT | 1218-1237 |
| `mod_registry` | adapter snapshot | `snapshot.sources`/`diagnostics` — but SPEC wants per-mod capabilities breakdown | DRIFT (shape) | 1241-1261 |
| `mods_reload` | `modAdapter.reload()` | Correct; pushes `mods_updated` | OK | 1265-1284 |
| `memory_repository_status` | git config `letta.memoryRepository.url` semantics (zc's previous impl) | Only checks `.git` dir exists — "enabled" ≠ has remote/push URL | DRIFT | 1288-1308 |
| `memory_repository_push` | harness push path (memory-repository-push machinery) | Raw `git push` in memfs root — bypasses hub push URL config nuances; probably works on wired agents | DRIFT | 1310-1332 |
| `list_memory` | `list_memory` native semantics incl. `include_references` | Hand-rolled recursive walk; declared `include_references` ignored; skips `.git*` only | DRIFT | 1336-1378 |
| `read_memory_file` | scoped read | `readFileSync(join(root, path))` — **no traversal guard** (`../../` escapes memfs) | DRIFT + security | 1380-1408 |
| `secret_list` | `/secret list` handler | Real handler; returns names only (masked by construction ✓) but as preformatted text, not structured | OK-ish | 1412-1430 |
| `secret_apply` | `/secret set` | Real handler ✓; note plaintext value transits the frame (fine locally; ZC_DEBUG_FRAMES on the client would capture it) | OK | 1432-1452 |
| `cron_list` | (no native module) | Shells out to **global `letta cron list --json`** — version-skewed binary vs fork; no trigger/rm frames at all (zc /crons regresses) | DRIFT + gap | 1456-1479 |
| `notification` | SPEC §5.8: **server→client push** at native trigger points | Implemented **backwards** as client→server request that's ack'd and dropped; no server-side notification emission exists | BROKEN (inverted) | 1483-1493 |
| `session_event` | hooks executor (`src/hooks/executor.ts`: JSON stdin, timeout, matchers, prompt hooks, block semantics) | Hand-rolled `exec(hook.command)` — field doesn't exist on `HookCommand` union (TS2339 L1507/1510) → **hooks never actually run**; only SessionStart handled; no Stop/UserPromptSubmit/SessionEnd anywhere | BROKEN/STUB | 1497-1521 |
| `command_catalog` | full registry + custom `.commands/*.md` + mod commands + `routing` metadata (SPEC §5.6) | Builtin registry only; fields `{id, desc, args, hidden, order, noArgs}` vs spec `{id, description, args_hint, routing, source, namespace}`; no custom cmds, no mod cmds | DRIFT + gaps | 1525-1551 |
| `agent_export` | native .af export | `{agent: retrieveAgent(), exported_at, version}` — **not an AgentFile export** | STUB | 1555-1577 |
| `conversation_fork` | `backend.forkConversation(conversationId, options)` — **exists** (backend.ts:473) | `createConversation({agent_id, hidden})` — creates an **empty** conversation, forks nothing | BROKEN | 1581-1603 |
| `conversation_update` | `backend.updateConversation` | Correct (title/archived/hidden) | OK | 1607-1629 |
| `agent_update` | `backend.updateAgent` | Correct | OK | 1633-1653 |
| `agent_retrieve` | `backend.retrieveAgent` | Correct | OK | 1657-1674 |
| `conversation_recompile` | `recompileConversation(conversationId, body?)` | Passes `(agent.id, conversationId)` — **agent id where conversation id goes** (TS2559 L1682) | BROKEN | 1678-1698 |
| `read_file` | permission-gated read | Unrestricted `readFileSync` of any absolute path | DRIFT + security | 1702-1720 |
| `search_files` | native file-search helper (@-completion matcher) | `` exec(`find . -name "${pattern}"`) `` — **shell injection via `pattern`**, no gitignore awareness, 100-result head | BROKEN + security | 1722-1746 |
| *(unknown)* | — | `error` frame, loop continues ✓ | OK | 1748-1750 |

**Frames the spec requires that have no handler/emission at all**: `overlay_state`/`overlay_event` (§5.5 — the entire interactive-command mechanism), `input_submit_response {routed}`, `input_paste`, `queue_defer_set`, `transcript_entry_detail`, `exit_stats`, `conversations_updated` push, `notification` push (server→client), `should_doctor`, `memory_updated`/`skills_updated` pushes, `device_status.pending_control_requests` (approval recovery — `getResumeDataFromBackend` imported and never used, TS6133 L31).

---

## 3. Transcript model: Stage 1 was not done

SPEC §5.1 is explicit: *"Clients receive constructed transcript entries; they never re-accumulate raw deltas."* The implementation:

- `transcript_update` emits **raw SDK chunks** (`chunk: chunk`, L504-509) with a comment still reading *"Stage 0: emit raw stream events; Stage 1 will emit constructed transcript entries"* — the Stage 1 transcript work is self-admittedly absent despite the commit title.
- `transcript_sync` fires **only on `end_turn`** (not on open/resume/reconnect/cancel/error), has no `conversation_id`, no `revision`, and maps entries to `{id, kind, text, otid}` — **dropping all tool fields** (name, args, result, status, diffs), usage footers, phases. Tool cards cannot be rendered from sync. (L529-542)
- No `TranscriptEntry` model, no `phase`, no truncation-with-`transcript_entry_detail` (clients are forced back to truncate-at-ingest, the exact zc quality-audit bug this design was written to kill).
- `session_id: agent.id` where spec says `conversation_id`.

## 4. Turn loop integrity vs `headless.ts`

Faithful: `createBuffers` → `sendMessageStream(conversationId, input, {agentId, preparedToolContext})` (matches headless.ts:647) → `drainStreamWithResume(stream, buffers, noop, signal, undefined, hook, tracker)` (signature verified) → classify → requestPermission per `needsUserInput` (mirrors headless.ts:4975-5030).

Divergences beyond F1–F4:

1. **Tool context loses mods**: ui-server's `prepareToolContext` omits `modContext`/`modEvents` that headless passes (headless.ts:456-465) → **mod-registered tools (job_run, ask_remote_agent, …) are absent from every turn**.
2. **`turnToolContextId` never threaded**: `getStreamToolContextId` imported, unused (TS6133) → `classifyApprovals`/`executeApprovalBatch` run without `toolContextId`, so the turn-scoped tool snapshot isn't used for arg-validation/execution.
3. **`classifyApprovals` options wrong**: `alwaysRequiresUserInput: false` (boolean where a function is expected, TS2322 L560 — silently disables the interactive-tool forcing headless applies via `isInteractiveApprovalTool`), and **no `permissionModeState`/`workingDirectory`/`agentId`** → permission modes (acceptEdits/unrestricted) can never influence classification.
4. **AskUserQuestion excluded** from the toolset (L215) — matches *headless*, but the seam contract is *Ink parity*; zc's question-form dialog is unreachable (no `ask_user_question` control_request can ever occur).
5. **`turn_end` always lies**: emitted with `stop_reason: "end_turn"` on every exit path including cancel and error (L631); a nonstandard `turn_cancelled` frame precedes it on aborts.
6. **Mod turn lifecycle absent**: headless emits `turn_start`/`turn_end` mod events (headless.ts:591/625); ui-server never does → muscle-memory-class mods are blind. `conversation_open`/`close` ARE emitted but with **2 args instead of 3** (TS2554 L287/L1755 — context stuffed inside the payload) → mods receive a malformed event and no context (breaks ZeptoCode jobs mod's `conversation_open` re-adopt).
7. **No queueing/no concurrent-input guard**: a second `input_submit` mid-turn just sits in the queue (accidental serialization — acceptable) but during approvals hits F4.
8. **`emitLocalToolReturns` never called** (headless.ts:5044) — even with F1/F2 fixed, tool *returns* would never reach the client.
9. **SessionStats never fed** → stats/context frames permanently zero.

## 5. Stdout hygiene

- ✅ Concept right: `console.log/info/warn` → stderr redirect; EPIPE → clean exit; single `writeFrame` chokepoint (L112-131).
- ⚠️ **Redirect installs too late for import side effects**: ES import hoisting means all static imports (backend, settings-manager, telemetry, mod machinery) evaluate **before** the module body runs the redirect. Any module-scope `console.log` in that graph hits raw stdout ahead of the guard. Low probability, real class.
- ⚠️ `_origLog/_origInfo/_origWarn` saved and never used (TS6133 ×3) — dead ballast.
- ⚠️ Non-EPIPE stdout errors are re-`throw`n inside an event handler → uncaught exception, hard crash without a frame.
- ⚠️ Had F1 not crashed first: `emitLocalToolCalls` writes **headless stream-json wire messages to stdout** — a different schema interleaved into the zc frame stream. The intent, not just the path, was wrong; local tool call/return surfacing must be zc `transcript_update` frames.

## 6. Error handling

- Malformed JSON on stdin → `error` frame, continue ✓. Unknown frame type → `error` frame ✓.
- **No try/catch around `processTurn`** or the switch dispatch: any exception in the turn path (F1/F2 guaranteed) unwinds `runUIServer` entirely → process exit. One bad handler kills the session, violating "emit an error frame and continue".
- Per-handler try/catches exist for the Stage 3–5 request/response handlers ✓, though response frame *names* are one-off inventions (`settings_response`, `hooks_response`, `skills_response`…) that don't match SPEC names (`settings_read_response`, …).
- Startup failures (agent not found, conversation not found) print to stderr and exit 1 **before `hello_response`** — a client not watching child-exit waits forever. No `hello_response {error}` path.

## 7. Security notes

1. `search_files`: shell **command injection** via `pattern` (double-quoted interpolation into `find`), L1728.
2. `read_file`: arbitrary filesystem read with no gating, L1702.
3. `read_memory_file`: `../` traversal escapes the memfs root, L1394.
4. `settings_read` redaction is a key-name heuristic on top-level keys only.
5. `secret_apply` value in the frame stream — fine for a private pipe; flag for client-side frame-dump tooling (ZC_DEBUG_FRAMES would capture it).

## 8. Structure / drift-rule (§2.3) violations

- 1,764-line monolith: types + hygiene + adapter + turn loop + 34 handlers in one file; every Stage 3-5 handler inlines `await import(...)` and hand-rolls response shapes. No extraction of React-bound logic into shared modules happened (the actual §2.3 mandate); where the native logic was React-bound, it was **reimplemented from imagination** instead: bash mode, hook firing, panel-render context, command routing, session_event.
- Hand-rolled where a native module exists: `search_messages` (→ `searchMessagesForBackend`), `conversation_fork` (→ `backend.forkConversation`), `agent_export` (→ native .af export), hook firing (→ `hooks/executor.ts`), panel eval (→ `renderModPanelLines`), file search (→ native file-search helper), `list_memory` walk.
- Dead imports: `getStreamToolContextId`, `getResumeDataFromBackend`, `ModCapabilities`, `_orig*` (TS6133 ×6) — each one marks a native mechanism that was planned and dropped (tool-context threading, approval recovery).

## 9. The shim + router

- `subcommands/ui-server.ts`: parseArgs OK-in-practice but all three value extractions are type-unsafe (TS2322 ×3, L53-55). Help/exit codes fine; `import.meta.main` direct-bun entry ✓; fatal handler prints to stderr ✓.
- Router registration correct, including `subcommandNeedsEarlyBackendMode` ✓.
- **--agent is mandatory** → there is **no agentless "lobby" mode**: a client cannot use `agents_list` for its *startup* picker because it can't start the server without already knowing an agent. This chicken-and-egg is a **design gap in the fork AND a hole in SPEC.md** — nothing defines how a client lists agents before the first spawn. Directly relevant to the startup-picker failure.

## 10. Stage claims vs reality

| Stage claim (commit) | Reality |
|---|---|
| S1 "mod_panels_query reads adapter snapshot registry, evaluates renders" | Registry access real; render **context is wrong** (numbers passed where functions/ModContext go) → real panels throw and vanish |
| S1 "bash output cached & injected as system reminder" | Hand-rolled exec, not native bash semantics; no shell transcript entry; `<bash-output>` only |
| S2 "routes through native command registry" | True but meaningless: 54/64 handlers are App.tsx placebos; mod/custom commands absent |
| S2 "command_catalog: 64 native commands with metadata" | Builtin-only, non-spec field names, no routing/source |
| S3 settings/hooks/session_event/notification | settings_patch bypasses the manager (race recreated); hooks_update no-ops (SaveLocation misuse); session_event hook exec references nonexistent field; notification direction inverted |
| S4 search/skills/context/stats/lists/models/update_model/… | search broken on local backend; stats always zero; models unstructured; update_model arg bug; agents_list `{items}` trap |
| S5 reflection/subagents/mod_registry/memrepo/memory/secrets/crons/export/fork/update/retrieve/recompile/files | fork isn't a fork; recompile args swapped; export isn't .af; subagents = runtime state; crons shell out to the global binary; files = injection |
| "Verified: hello, input_submit, command_catalog, settings_read, session_stats, agents_list, bash, slash" | Exactly the subset that never touches tools, approvals, panels, interrupts, or a second in-flight frame |

## 11. What is genuinely right (do not re-litigate when fixing)

- Additive-mode architecture per R13: untouched Ink TUI, router wiring, `TUI_MOD_CAPABILITIES` via `createModAdapter` with agent-scoped mods dir (matches listener fix #3344 pattern), `installLocalBackendModEventHooks` (correct signature), `persistSession`, conversation create/resume flow, `computeDiffPreviews` for approval diffs, `classifyApprovals`/`drainStreamWithResume`/`createBuffers`/`sendMessageStream` as the shared-module backbone, secret handlers, `discoverSkills`, `loadHooks`, readline queue + EPIPE handling, per-handler error frames, unknown-frame error. The skeleton is salvageable; the Stage 1-5 flesh mostly is not.

## 12. Verification gate for any fix pass

`bun run typecheck` must be **zero-error** on the branch (currently 26, all in the two new files; upstream clean) — half the fatal findings above were flagged by tsc and would have been caught before any runtime test. Then a gauntlet that includes: a tool-using turn, an approval with a concurrent panels poll, an esc during streaming, a mode switch followed by an auto-approvable edit, and an `agents_list` against a realistic agent set (frame-size + shape).
