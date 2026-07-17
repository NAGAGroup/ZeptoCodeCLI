# Thin-client first pass: full audit (both sides of the seam)

**Date**: 2026-07-16
**Scope**: commit 2837ec6 (ZeptoCodeCLI Go rewrite, "all 5 phases complete") + fork commit 89b793a9 (`zc-ui-server` branch, "Stages 1-5"), audited against `docs/protocol/SPEC.md` + `GO-THIN-CLIENT-SPEC.md`.
**Detail reports** (exhaustive, per-frame/per-file, with line numbers): [audit-thinclient-zc-go.md](audit-thinclient-zc-go.md) · [audit-thinclient-uiserver-ts.md](audit-thinclient-uiserver-ts.md)
**Method**: two source-level deep dives (one per repo) + live reproduction in tmux against the probe agent + direct stdio probes of the ui-server.

---

## 1. The reported symptom, root-caused (verified live)

`zc` with no `--agent` fails **deterministically**:

1. **Go: picked agent never propagates.** `startRuntimeCmd(agent string)` ignores its parameter and builds opts from `m.startOpts`, whose `.Agent` is never set by the picker (only `ConversationID` is). `ConnectStdio` rejects empty Agent → **"startup failed: stdio: Agent is required"** after the conversation pick. Reproduced in tmux, 100%. This is the 1ebe567 bug class (pre-runtime UI choice not reaching client opts) reintroduced.
2. **Go: the picker's data path is double-broken but masked.** `letta agents list --json` — the flag doesn't exist (exit 1); even without it, output is `{"items":[...]}`, not the bare array Go decodes. The picker only renders at all because of a silent fallback that raw-reads `~/.letta/lc-local-backend/agents/*.json` (unsorted, includes hidden/scratch agents — the wall of "Letta Code" rows).
3. **Design gap under both bugs**: the ui-server **requires `--agent` at spawn**, so the specced `agents_list` frame can't serve the *startup* picker (chicken-and-egg). SPEC.md never defines pre-spawn agent listing. Needs a spec decision: lobby/agent-optional spawn mode, or a sanctioned pre-spawn listing mechanism.

Additional live-verified startup damage: each launch spawns 2–3 ui-server children (one **leaks** and runs until exit) and abandons ~2 empty conversations per launch (observed conv-276..286 accumulating) — the litter bug /tidy + 1ebe567 fixed, resurrected.

## 2. State of the build beyond the picker (the picker is not the problem)

Fixing §1 gets you to a chat that handles **plain text turns only**. Verified live: PONG round-trip works; `/usage` prints "Fetching usage statistics..." and dies (placebo registry handler + stats never fed); `/model` does nothing. The deep dives establish:

- **Any tool-using turn crashes the ui-server** (nonexistent module import + invented `executeApprovalBatch` signature). The fork branch has **26 TypeScript errors** (`bun run typecheck`; upstream clean) — several prove dead/crashing code on core paths.
- **`transcript_sync` means different things on each side**: server sends "this turn's buffers at end_turn"; Go treats it as the authoritative full transcript and **wipes everything rendered** after every completed turn. Classic two-model seam bug.
- **Esc-interrupt is dead on both sides independently** (Go: `loopStatus` never updated → `turnActive()` always false → interrupt unreachable, spinner never spins, ctrl+c arming broken, statusline always "ready"; server: abort controller provably null at the handler, no fast-path read).
- **Approvals livelock**: any frame arriving during the server's approval wait spins the unshift/shift loop at 100% CPU; Go doesn't gate input during turns, so the trigger is live.
- **54/64 slash commands are placebos** (native registry handlers are App.tsx stubs — "Opening agent browser..." strings; the §2.3 extraction work was skipped); **mod commands (/jobs, /audit) don't route at all**; `execute_command` builds input without the leading slash (masked today by another bug).
- **Permission modes are cosmetic end-to-end** (server echoes and discards mode; classification never sees it).
- **Mod-registered tools (job_run, ask_remote_agent, …) are absent from every turn** (tool context drops modContext).
- **Phase 3 was not delivered**: zero Go call sites use the Stage 3–5 management frames; `overlay_state` has no producer; management UX is gone with no working replacement. Periodic idle sync (commit 20961ce) was **spec-only — no code**.
- Both "Verified:" lists in the two commits are exactly the happy paths that avoid tools, approvals, panels, interrupts, and concurrent frames.

**What's genuinely sound** (keep, don't re-litigate): the additive ui-server mode architecture (untouched Ink TUI, router wiring, TUI_MOD_CAPABILITIES, shared-module turn backbone: `sendMessageStream`/`drainStreamWithResume`/`createBuffers`/`classifyApprovals`, `computeDiffPreviews`), stdout-hygiene concept, per-handler error frames; Go side: stdio transport skeleton with request_id correlation, phase machine shape, all the rendering machinery, and the survived quality items (batch-drain, drift warning, empty-conv honesty, KeyRelease swallowing, esc-priority chain).

## 3. Why the first pass failed (patterns to enforce against next pass)

1. **Demo-driven verification.** Both commits verified only the paths in their own "Verified:" lists. No tool call, no approval, no interrupt, no picker-to-chat flow was ever exercised.
2. **Placebo integration.** Calling a real-looking API (`executeCommand`, registry handlers) whose native implementations are UI-side stubs — the work *was* the extraction, and it was skipped on both sides (server: App.tsx logic; client: Phase 3 frame wiring).
3. **Type checker ignored.** 26 tsc errors on the branch; roughly half the fatal server bugs were flagged by tsc before any runtime test.
4. **Invented signatures instead of read ones** (`executeApprovalBatch` 6-arg call, `saveHooksToLocation` path-for-enum, panel render context, hook `.command` field) — the drift rule (§2.3: import plain modules, extract React-bound ones) was replaced by reimplementation from imagination.
5. **Seam words not pinned to shapes.** `transcript_sync` (and `agents_list` `{items}` vs `agents`) diverged because the frame shapes live in prose, not in a single shared source of truth checked by both sides.
6. **Spec holes surfaced by implementation** (startup agent listing; loop-status/turn-activity signal) were silently worked around instead of flagged.

**Proposed gates for the fix pass** (mechanical, cheap):
- Fork branch: `bun run typecheck` zero-error, enforced before any runtime claim.
- Frame shapes: one canonical table (SPEC.md appendix or generated types) that both sides cite; any new/renamed frame updates it in the same commit.
- Gauntlet minimum: no-agent launch → pick → chat; tool-using turn with approval (+ concurrent panels poll); esc during streaming; mode switch then auto-approved edit; /jobs (mod command); process/conversation count before vs after (no litter, no leaks).

## 4. Consolidated fix tracker (exhaustive — everything gets done; ordering is a suggestion)

IDs reference the detail reports (GO-* = audit-thinclient-zc-go.md, TS-* = audit-thinclient-uiserver-ts.md sections).

### Batch A — startup + protocol contract (unblocks everything)
- [ ] A1 Propagate picked agent into startOpts / use `startRuntimeCmd`'s param (GO §1 primary)
- [ ] A2 Decide + spec pre-spawn agent listing (lobby mode / agent-optional spawn / sanctioned headless list); implement accordingly; delete `listAgentsHeadless`/`listAgentsFromBackend` or bless a corrected version (GO §1, §5; TS §9)
- [ ] A3 Stop the spawn storm: one connection through startup (list conversations over the *same* child that becomes the runtime, or spawn with `--conversation` chosen pre-spawn); close-or-reuse, never abandon (GO §1 leak + litter)
- [ ] A4 ui-server: don't create a conversation on spawn until the client commits to one (or accept a `--no-conversation` listing mode) (litter root)
- [ ] A5 Fix hello race (register pending channel before send) (GO §3)
- [ ] A6 Emit `hello_response {error}` / structured failure instead of stderr+exit before handshake (TS §6)
- [ ] A7 UIServerPath/UIBin: config/flag/env instead of hardcoded dev path (GO §3)
- [ ] A8 Canonical frame-shape table both sides cite; fix `agents_list` response (`agents: [...]` summaries, not `{items}` full AgentStates) (TS F5)
- [ ] A9 Startup esc trap: allow reopening pickers (GO §1 SMELL)

### Batch B — core chat correctness
- [ ] B1 Define `transcript_sync` semantics (authoritative snapshot vs per-turn); implement the same meaning on both sides; sync on open/resume/cancel/error too (TS §3, GO §2)
- [ ] B2 Real TranscriptEntry model server-side: tool fields, phases, usage — Stage 1 as actually specced (TS §3)
- [ ] B3 Fix tool-call crash: correct module path + real `executeApprovalBatch` signature + native continuation shape + `emitLocalToolReturns` as zc frames (TS F1, F2, §5 last bullet)
- [ ] B4 Fix approval wait livelock (event-driven wait, not unshift-spin); handle interleaved frames during approvals (TS F4)
- [ ] B5 Interrupt: fast-path read in line handler (headless.ts:3998 pattern) + keep controller alive; Go: turn-activity signal (`turn_start`/`turn_end` should drive `turnActive`, not dead protocol_v2 loopStatus) → esc-interrupt, ctrl+c arming, spinner, statusline, input gating during approvals (TS F3, GO §4 cluster)
- [ ] B6 Stdio client: never silently drop frames (block with backpressure or grow buffer + error loudly); fail pending requests on child death (GO §3)
- [ ] B7 `turn_end` honest stop_reasons (cancelled/error); Go decode `turn_cancelled` or drop the frame (TS §4.5, GO §2)
- [ ] B8 Switch paths: guard `disconnectedMsg` during `m.switching` (no spurious reconnect/zombie); serialize `m.cli` mutation via Update-thread messages (GO §8)
- [ ] B9 Permission mode: store + thread `permissionModeState` into classification server-side; carry mode at spawn/hello (`--mode` currently a lie) (TS change_device_state row, GO §3)
- [ ] B10 classifyApprovals options: real function/`workingDirectory`/`agentId`/toolContextId threading (TS §4.2-3)
- [ ] B11 Re-include AskUserQuestion (Ink parity, not headless parity) → question form works via control_request `updated_input` (TS §4.4)
- [ ] B12 Mod tool context (modContext/modEvents) in every turn → job_run etc. return (TS §4.1)
- [ ] B13 Mod lifecycle events: turn_start/turn_end emission + 3-arg conversation_open/close (jobs mod re-adopt depends on it) (TS §4.6)
- [ ] B14 SessionStats actually fed per turn → session_stats/context_window_overview real (TS stubs)

### Batch C — command surface
- [ ] C1 The extraction work: overlay-producing server-side implementations for the App.tsx-handled commands (`overlay_state`/`overlay_event` machinery, §5.5) — the heart of Phase 2/3 on both sides (TS F6, GO §6)
- [ ] C2 Route mod + custom commands through input_submit/execute_command (adapter command registry + `.commands/*.md`) (TS F6)
- [ ] C3 `execute_command` leading-slash fix + propagate real `success`/`notFound` (GO §2, TS F6)
- [ ] C4 Go fetches `command_catalog` (palette/completions repopulated); spec-shape fields incl. routing/source (GO §4, TS catalog row)
- [ ] C5 Native bash-mode semantics (extract, don't hand-roll): shell transcript entry, native reminder injection, cwd/alias behavior (TS bash row)
- [ ] C6 Fix broken CRUD handlers: conversation_fork → `backend.forkConversation`; conversation_recompile arg order; update_model handle+scope+updateArgs; hooks_update SaveLocation; search_messages → `searchMessagesForBackend`; agent_export → real .af; subagent_defs vs runtime state; models_list structured catalog (TS table)
- [ ] C7 Management UX decision per command (overlay descriptors vs text) and Go wiring for the Stage 3–5 data frames it keeps (secret/memory/crons/skills/mods/hooks/profiles…) — restore interaction models lost in the gut (GO §6)
- [ ] C8 /export via `agent_export` frame; @-completion via `search_files`/`read_file` (injection-fixed) or explicit spec carve-out (GO §5, TS security)
- [ ] C9 settings_patch through settingsManager (scope routing, no raw writeFileSync); settings_read spec shape + revision (TS settings rows)
- [ ] C10 Approval extras: permission_suggestions computed server-side + `selected_permission_suggestion_ids` sent from Go; custom deny message; `pending_control_requests` recovery (GO §2/§4, TS gaps)
- [ ] C11 Periodic idle sync (G6/20961ce): actually implement client-side (idle-only, 15–30s, full query + diff)
- [ ] C12 Notification direction fixed (server→client push at native trigger points); should_doctor, conversations/memory/skills_updated pushes as specced (TS notification row)
- [ ] C13 mod_panels_query with real ModPanelRenderContext (functions, chalk, agent/model) or `renderModPanelLines` reuse; Go actually queries panels (both sides currently dead) (TS panel row, GO §4)
- [ ] C14 session_event → real hooks executor; port the 5 client-loop hook call-sites per the seam plan (TS session_event row)

### Batch D — hygiene, security, traps
- [ ] D1 Security: search_files injection; read_file gating; read_memory_file traversal guard (TS §7)
- [ ] D2 Kill dead code: legacy protocol_v2 frame types Go decodes but nobody sends; pager/jobs-overlay/mgmt remnants without producers; modelSwitchedMsg nil-deref trap; dead flags (--port, --mode until real); dead imports server-side (TS §8, GO §§4-7)
- [ ] D3 IO out of Update: /title, /rename, /model round-trips as tea.Cmds (GO §8)
- [ ] D4 pixi.toml: remove `spike`, fix `smoke` (vacuous), env-driven stdio-smoke agent id (GO §9)
- [ ] D5 keymap/help text truth (/jobs claim etc.) (GO §6)
- [ ] D6 Monolith split server-side (types/hygiene/turn-loop/handlers modules); stdout redirect before import side effects (dynamic-import boot shim) (TS §5, §8)
- [ ] D7 stdio scanner cap + oversized-frame policy, unknown-request error frames with request_id (GO §3)
- [ ] D8 agent_retrieve model field (`llm_config.model`); statusline nil guard; opts mutation sync (GO §§2-8 smells)
- [ ] D9 ui-server log: per-process identification (pid/timestamps) — the shared append log made the live "Fatal: Settings not initialized" untraceable
- [ ] D10 Tests that would have caught this: seam round-trip tests per frame (Go ↔ fork), no-agent startup integration test, tool-turn + approval gauntlet in stdio-smoke

## 5. Suggested execution order

A (startup + contract) → B (core chat) → C1–C5 (command surface heart) → C6–C14 → D throughout as touched. Batches A+B restore "daily-drivable for plain work"; C restores the feature bar; D keeps it honest. Ordering is a suggestion — everything on the list gets done.
