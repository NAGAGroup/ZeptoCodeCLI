# zc UI Protocol Specification (draft 5 — native React primitives, 1:1)

**Status**: draft for review · **Base**: letta-code 0.28.9 (`d253471e`) · **Ground truth**: the native Ink TUI source (`src/cli/app/AppCoordinator.tsx` and the components it renders) + [TOUCHPOINTS.md](./TOUCHPOINTS.md)

> **Draft 5 (this document)** locks the model per Jack (2026-07-17): *model the native app exactly.* The native TUI is React; so is our client. The protocol is nothing more than **the native TUI's own state (its React primitives) serialized 1:1 as JSON that the client handles, plus a set of JSON event types the client can send to the server.** No invented taxonomy, no wire optimizations the native app doesn't have as concepts. Drafts 1–4 progressively overbuilt this (capability-negotiated superset → headless-React seam → state-slice catalog with upsert/revision machinery); draft 5 deletes the ceremony.
>
> **Source discipline:** ground every claim in the letta-code source (read it), not in the Letta Ecosystem Expert — it operates at the SDK/docs level and hallucinates code internals (Jack, 2026-07-17). The audit reports (`../audits/audit-thinclient-*.md`), which were produced by reading the source, are the reliable prior.

## 1. The model

The native app is `AppCoordinator.tsx`: a pile of React state (`useState`/`useSyncExternalStore`) that the UI renders from —

```
const [lines, setLines]                     = useState<Line[]>([]);              // the transcript
const [pendingApprovals, setPendingApprovals] = useState<ApprovalRequest[]>([]); // the approval dialog
const [queueDisplay, setQueueDisplay]       = useState<QueuedMessage[]>([]);
const [thinkingMessage, setThinkingMessage] = useState(...);
const [agentState], [conversationId], [uiPermissionMode], [networkPhase], [executionPhase],
      [tokenCount], [usedContextTokens], [activeOverlay], [currentModelId], ...  // ~50 pieces
```

> **P1. The protocol IS this state.** The server holds the native app's state below the seam and pushes each piece as JSON when it changes. The client keeps a copy and renders from it. **Every `useState` in the native component is a thing the server pushes; that is the entire server→client half of the protocol.**
>
> **P2. The client is the render half of the same component.** It reacts to incoming JSON exactly as React re-renders on `setState`. Its frame handler is a reducer `(model, json) → model`; all UI flows from the held state. It never cares *why* a push arrived.
>
> **P3. The client→server half is a small set of committed JSON events** — a submitted message, a selection result, an approval decision, a lifecycle signal, a query. That's it.

### The three rules (Jack, 2026-07-17)

1. **Selection happens client-side.** The server provides options (as state); the client presents them however it wants and sends back the committed choice. There are no server-driven pickers/overlays/forms.
2. **The server sees only committed requests from the client — no streaming.** Client→server is discrete events. The server holds no partial-input state.
3. **Anything that would require streaming to the server is handled by the client** — but it MAY still use the server protocol for help. Canonical example (§3).

## 2. Architecture (unchanged from draft 3/4)

- **Fork** = upstream at the installed release tag **plus one additive mode** (`letta ui-server`). Ink TUI untouched. Only permitted edits to existing files: behavior-preserving **extractions** of React-bound policy into plain shared modules that both the Ink hook and the mode consume (never a parallel reimplementation — every first-pass hand-rolled "equivalent" was wrong).
- **No app-server, no listener, no ports.** The harness runs in-process in the spawned child, exactly like the native TUI in local mode. (Client startup copy MUST NOT say "starting app-server" — there isn't one.)
- **Server built like `headless.ts`**: a React-free host over the same modules (`sendMessageStream`, accumulator, command registry, settings-manager, mod engine with `TUI_MOD_CAPABILITIES`). It drives the native state below the seam and serializes it.
- **The server is itself reactive**: the main loop is a single stdin dispatcher that **never blocks waiting for a specific frame**. Turn processing runs concurrently; waits for user decisions are promises the dispatcher resolves when the event arrives. A blocking read-for-one-frame loop is forbidden (first-pass F4: approval livelock at 100% CPU).
- **Transport = stdio** (JSON-lines on stdin/stdout, logs on stderr). One client per server by construction; no ports/daemons/reconnect (child dies = session ends; a fresh child resumes conversation state from storage). **Stdout hygiene is a hard rule**: frames own stdout exclusively; the redirect guard installs before any harness import evaluates (dynamic-import boot shim — ES import hoisting defeats a module-body guard).
- **Schema artifact = the wire contract**: one TypeScript file in the fork, `src/zc/protocol.ts`, is normative for every JSON shape. The Go client generates/transliterates from it and cites it by commit; any shape change lands in the same commit. *Prose (this doc) carries semantics; the artifact carries shapes.* This is the countermeasure to the first-pass failure where two models derived different shapes (`transcript_sync`, `agents_list {items}`) from the same prose.

## 3. Interaction: reactive client, async input, the slash pattern

The client is a set of reactive handlers over the incoming JSON stream **plus local UI components that operate independently**. Local editing never blocks on a server round-trip; server pushes cause reactive UI transitions. The canonical example (Jack's, verbatim), which every "would-need-streaming" interaction follows:

**Slash-command completion:**
1. User types `/` → client fires a query for available commands. Fire-and-forget.
2. **The input field keeps operating asynchronously** — it's just a text field; it doesn't know about the query.
3. Server pushes the available slash commands (as state).
4. The client's reactive handler catches that push and **brings up the slash-command selector UI** (halting input into the field while the selector is active — a client-side UI transition).
5. User selects → the selector **fills the input box** → user continues typing args (or just presses enter).
6. On enter, the client ships **raw text** to the server; the server routes/executes it as it should.

Generalization: the client owns all pre-submission interaction (editing, filtering, selection); the server owns interpretation of *committed* input. `@`-file completion is the same shape but resolved **client-side against the local filesystem** (§4), since streaming keystrokes to the server for path matching is exactly what rule 3 forbids — stdio guarantees the client shares the server's filesystem, so the client reads it directly (resolving against the server cwd from state; the one documented place client logic touches domain state — a future remote transport would break here).

Queries are always fire-and-forget: send, keep rendering; the resulting push is handled like any other. **No blocking waits, no pending maps, no transport timeouts** (the entire first-pass request/response machinery is deleted, not ported). A query MAY carry `request_id` echoed as `in_reply_to` **for debugging/loading affordances only** — client behavior must never depend on it. A feature wanting a "still waiting / no response" UX starts its **own client-side timer** on query and clears it when the state arrives (bubbletea: query + `tea.Tick`; the state's reducer case clears the pending flag). The protocol has no timeout concept.

## 4. Server → client: the native state, serialized

The set below **is** `AppCoordinator`'s state, modeled 1:1. Shapes are normative in `src/zc/protocol.ts`; here each piece names its native origin. The client renders from these exactly as the native component does.

| State (`type`) | Native origin (`AppCoordinator.tsx` / component) | Notes |
|---|---|---|
| `transcript` | `lines: Line[]` (L2286) | **The whole transcript, pushed as the native `Line[]`.** Client parses/renders/diffs it however it wants. No upserts/deltas/revisions/entry-detail — those were invented. Tool output is carried in full; the **client truncates at render** (expand shows more) exactly as the native component does (also the audit's fix for truncate-at-ingest). |
| `pending_approvals` | `pendingApprovals: ApprovalRequest[]` + `approvalContexts` (L545/548) | The approval dialog's state. Client renders the dialog when non-empty. AskUserQuestion is an `ApprovalRequest` carrying questions. A restarted client rendering outstanding approvals is just "render from pushed state" — not a special recovery mechanism. |
| `turn` | `networkPhase`/`executionPhase`/`isExecutingTool`/`bashRunning`/`interruptRequested`/`thinkingMessage` (L477–L521, L1045) | Drives spinner, statusline activity, input gating, ctrl+c arming, title blink. (First-pass had no such state → esc/spinner/ctrl+c/gating all broke.) |
| `queue` | `queueDisplay: QueuedMessage[]` + defer state (L1405) | Typed items; defer flag. |
| `subagents` | `@/agent/subagent-state` snapshot | Nested transcript entries carry the tree. |
| `device` | `agentState`/`agentId`/`conversationId`/`conversationSummary`/`uiPermissionMode`/`currentModelId`/`currentModelHandle`/`currentToolset`/`usedContextTokens`/`tokenCount` + git/bg/should_doctor | Ambient status (statusline, header). Permission mode is **enforced below the seam**, not echoed-and-discarded. |
| `overlay` | `activeOverlay` + the selector's data (`modelSelectorOptions`, agent list, conversation list, skills, etc.) | Per rule 1 the server provides the **data**; the client owns the picker UI. `activeOverlay` tells the client *which* dataset is current; presentation is the client's. |
| `command_catalog` | registry + custom `.commands` + mod commands (SlashCommandAutocomplete's source) | Pushed in response to the `/`-query (§3), or proactively; client renders the selector. Includes description, args hint, routing class, source. |
| `settings` | `settingsManager` effective view | Merged view, secrets redacted; scopes/files invisible. Runtime is sole writer (kills the cache race). |
| `models` | `available-models`/`model-handles` catalog | Structured (id, handle, label, reasoning variant, key-missing) so the client can build a picker — first-pass had only a preformatted string. |
| `skills` / `hooks` / `crons` / `secrets` (names only) / `mod_registry` / `subagent_defs` / `memory` / `stats` (session + context) / `search_results` | respective native stores/dialogs | Pushed on query or change. Same 1:1 rule: whatever the native component holds, serialized. |
| `mod_panels` | `renderModPanelLines(panel, width, ctx)` evaluated below the seam | Client-polled (`mod_panels_query {width}`) — matches native render-on-every-frame; server evaluates real registered panel renders with the full native render context (first-pass passed numbers where functions go → panels threw). |
| `notification` | `app/notifications.ts` trigger | Client emits bell/OSC; focus policy client-side; Notification hooks run server-side at emit. |
| `toast` | — | Transient client message (query failures, info); TTL client-side. |
| `session_phase` | app boot / conversation-switch state | `lobby` \| `chat` + current agent/conversation (§6 lobby). |

**Change signals** (tiny pushes that say "this state is stale, re-query if you're showing it"): `settings_updated`, `mods_updated`, `memory_updated`, `skills_updated`, `crons_updated`, `conversations_updated`. Handled uniformly (P2).

## 5. Client → server: committed events

Discrete JSON events; committed intent only; none block. Shapes normative in `src/zc/protocol.ts`.

| Event (`type`) | Meaning |
|---|---|
| `hello` | handshake (`hello_response` carries runtime_build, version, and `error?` on fatal startup) |
| `input_submit {text, attachments?}` | the submit event — **raw text**; the server routes message / `/command` / `!bash` natively (client interprets nothing). Enter after the slash selector fills args (§3) just sends the assembled raw text. |
| `input_paste {content_kind, text? \| image_base64?}` | register a paste → server returns a placeholder (and image_ref); buffer holds the placeholder verbatim; resolution server-side on submit |
| `approval_response {id, decision, selected_permission_suggestion_ids?, updated_input?, message?}` | answer a `pending_approvals` item (message = custom deny text; AskUserQuestion answers ride `updated_input`) |
| `selection_result {overlay, choice \| choices}` | the committed result of a client-side selection (model/agent/conversation/skill/…). Rule 1: client selected; this is the commit. |
| `interrupt` | esc — abort the active turn (server fast-path reads it mid-turn; subagent cascade below the seam) |
| `change_mode {permission_mode}` / `change_cwd {cwd}` | shift+tab / `/cd`; enforced below the seam |
| `queue_defer_set {deferred}` | Ctrl+D defer toggle (server-held) |
| `execute_command {command_id, args?}` | explicit command execution (equivalently, `input_submit` with `/text` — server routes either) |
| `settings_patch {patch, expected_revision?}` | write settings (runtime is sole writer) |
| `hooks_update {...}` / `secret_apply {name,value}` / `memory_repository {...}` / `agent_export {...}` / `subagent_defs_write|delete` / `conversation_fork` (real `backend.forkConversation`) / `conversation_update` / `agent_update` | CRUD/actions the native dialogs perform |
| `select_agent {agent_id}` / `select_conversation {id \| "new"}` | lobby + switching (§6) |
| `session_event {event, duration_ms?}` | client lifecycle (session_start/end, conversation_open/close) → drives SessionEnd/lifecycle hooks + mod events server-side (best-effort session_end on disconnect) |
| `*_query {request_id?}` | refresh a state piece: `commands_query` (the `/`-trigger), `models_query`, `agents_query`, `conversations_query`, `skills_query`, `hooks_query`, `crons_query`, `settings_query`, `search_query {query,...}`, `stats_query`, `context_query`, `memory_query {...}`, `mod_panels_query {width}`, `mod_registry_query`, `subagent_defs_query` |

## 6. Lobby: one process for the whole lifecycle

`letta ui-server` starts **without requiring `--agent`**. Agentless it enters `lobby`: backend + settings init (nothing agent-scoped), pushes `session_phase{lobby}` + the agent list, waits. Client renders its picker → `select_agent` → server pushes that agent's conversation list → `select_conversation {id | "new"}` → server creates/resumes, boots agent scope (mods, memfs), pushes `session_phase{chat}` + `device` + `transcript`. CLI `--agent`/`--conversation` merely pre-answer these steps (no separate code path). **One process for the whole lifecycle → no respawn storm, no leaked children, no conversation litter** (first-pass A1–A4 dissolve). Switching later = the same events; the server reboots agent scope in-process, **or** the client respawns the child (both must behave identically — pick the simpler first; lobby makes respawn cheap and litter-free).

## 7. Client-side residue (deliberate, complete)

Anything not in §4/§5 is server-side. The client owns:

- **Text editing** (textarea, cursor, wrapping, kill-ring, undo, input-history recall) and **all completion/selection UI** (data from `command_catalog`/`models`/etc. state + same-machine `@` FS reads). Buffer *interpretation* is server-side.
- **Rendering**: markdown/glamour, diffs, tool cards, subagent tree, spinners/animations, token-display pacing, panel *placement* (lines arrive evaluated), toasts, ExitStats screen (data served).
- **Terminal integration**: keybindings, focus tracking, clipboard read/write (paste *content* → `input_paste`), bell/OSC on `notification`, window-title composition, resize, capability probing.
- **Opt-in per-feature timers** (§3), ctrl+c double-press arming, KeyReleaseMsg swallowing, client frame-dump debug (`ZC_DEBUG_FRAMES`).

## 8. Security & privacy

- `settings` redacts secret-bearing keys; `secrets` state carries names only; secret *values* only on the `secret_apply` event, over the private stdio pipe.
- `mod_panels` lines may carry mod ANSI; clients sanitize (SGR only; no cursor movement honored).
- No client-facing arbitrary file read/search/write frames (dropped) — the `@`-completion carve-out is client-local FS reads, not a server file frame.
- Single client per server by construction; the server serializes all state writes internally.

## Appendix A — Staged plan

Fork: `NAGAGroup/letta-code` (private, Apache-2.0) @ the installed release tag, branch `zc-ui-server`. **Gates per stage** (first-pass lessons): `bun run typecheck` zero-error on the fork; `src/zc/protocol.ts` updated in-commit with any shape change; tmux gauntlet against the probe agent including a **tool-using turn + approval + a concurrent `mod_panels_query`**, esc mid-stream, no-agent lobby startup, and a **process/conversation litter+leak count (before == after)**. Verify server internals in source (not from the expert) at implementation time.

- **Stage 0 — reset to the native-state model.** Establish `src/zc/protocol.ts`. Rewrite the ui-server loop as the single non-blocking dispatcher. Lobby + `session_phase` + agent/conversation lists + `select_*` (one-process lifecycle). Prove: agentless spawn → lobby → pick → chat, zero litter/leaks, clean stdout. *Directly fixes the reported symptom and deletes the whole picker/spawn/transport bug class.*
- **Stage 1 — core chat, correct.** `transcript` = native `Line[]` pushed whole; `turn` state driving spinner/gating/interrupt; `input_submit` (message route); `pending_approvals` + `approval_response` with a **tool-using turn that actually runs** (fix crash-on-tool: real module path + real `executeApprovalBatch` signature + native continuation — verify signatures in source); `interrupt` mid-turn fast-path; `mod_panels` polling with the real render context; permission mode enforced. Exit: zc renders a live streamed tool turn, answers an approval, esc-cancels, renders muscle-memory's panel.
- **Stage 2 — input policy + commands.** Bash route (`shell`-kind transcript entries + reminder injection, extracted); `command_catalog` (registry + custom + **mod** commands) + the `/`-query → selector flow (§3); `execute_command`; `overlay` data for model/agents/conversations/skills → client pickers → `selection_result`; `input_paste`; `queue`/`queue_defer_set`; AskUserQuestion (tool re-included).
- **Stage 3 — settings, hooks, lifecycle, notifications.** `settings`/`settings_patch`/`settings_updated` (sole-writer); `session_event` (SessionEnd + mod lifecycle, 3-arg conversation events); `notification` + Notification hooks at native trigger points; `hooks`/`hooks_update`. **Extract the 5 client-loop hook call sites** (Stop/UserPromptSubmit/SessionStart/SessionEnd/Notification) into shared modules (upstreamable = the #1282 fix).
- **Stage 4 — feature surfaces.** `search_results`, `skills` (+ honest `current_available_skills`), `stats` (session + context + history), `conversations_updated`, `models` (structured), `memory` viewers.
- **Stage 5 — long tail + sweep.** `subagent_defs`, reflection launch, `memory_repository`, `agent_export`, `mod_registry`, real `conversation_fork`, release-notes, /feedback//palace. Sweep TOUCHPOINTS for any row not exercised; close or re-classify each.

**zc (Go) demolition track** (parallel): each stage deletes the Go it obsoletes and replaces the request/response transport with the reducer + state store — pending maps, blocking `request()`, IO-in-Update, litter/leak/reconnect races go first (Stage 0); accumulator/ingest truncation (Stage 1); client command registry + custom pickers (Stage 2); settings/hooks file access (Stage 3); jsonl /search + `letta dream`/`node -e` shell-outs (Stage 4/5). End state: client = editing + rendering + keybindings + terminal integration, exactly §7.

## Appendix B — Decisions

| # | Decision |
|---|---|
| R7 | Plain native-style names; new protocol, nothing to collide with |
| R9 | Transport = stdio, one child per client, no reconnect; stdout hygiene |
| R13 | Server = additive protocol mode (`letta ui-server`), Ink untouched, **no app-server**; React-free host like `headless.ts`; React-bound residue **extracted** into shared modules, never reimplemented |
| **R14** | Queries fire-and-forget; blocking request/response abolished (deletes first-pass pending-map/timeout/IO-in-Update bug family) |
| **R15** | `src/zc/protocol.ts` normative for shapes; prose carries semantics; changes land in-commit; Go generates/cites it (countermeasure to two-models-two-shapes) |
| **R16** | No server-driven overlay/form descriptors; server provides options as state, client owns all selection presentation |
| **R23** | **Selections are stateless (the lobby pattern generalized), NOT a "pending_selections" server-state concept** (Jack, 2026-07-17). Client asks (query, or by running a picker command like `/model`) → server returns ONE rich `selection` frame → client picks → client sends the result → server acts. Cascades = the next query (provider → models, like agent → conversation). The rich `selection` type is expressive enough for ALL pickers: grouped items (pinned vs local agents; recent/recommended/all models), multi-select, and multi-question tool calls (AskUserQuestion). One selection type, one client renderer. The ONLY server-blocked decision is a tool approval / AskUserQuestion (mid-turn) → stays `pending_approvals` (AskUserQuestion reuses the selection shape for its questions but arrives inside `pending_approvals`). `pending_selections` is DELETED. |
| **R24** | **Mod commands are ordinary slash commands the server handles** (Jack, 2026-07-17): returned in `command_catalog`, executed server-side (builtin registry OR mod engine); the client cannot and must not distinguish them. |
| **R17** | `@` completion client-side (same-machine carve-out, resolved against server cwd); client-facing file-read/search frames dropped |
| **R18** | Agentless lobby mode; one process for the whole lifecycle (dissolves the startup chicken-and-egg + picker/spawn/litter/leak class) |
| **R19** | Server is reactive: single non-blocking stdin dispatcher; decision-waits are dispatcher-resolved promises (kills the approval livelock) |
| **R21** | **Model the native app exactly** (Jack, 2026-07-17): the protocol = `AppCoordinator`'s React state serialized 1:1 (client handles) + committed JSON events (client sends). No invented slices, no wire optimizations the native app lacks as concepts. Transcript = full `Line[]` push; approvals = the `pendingApprovals` array; etc. When in doubt, read the native component and copy its state shape. |
| **R22** | **Don't use the Ecosystem Expert for source internals** — SDK/docs level only; it hallucinates code. Read the source (or a source-reading subagent). |

### Open

- **Q-N9** Keep a raw `stream_delta` debug channel, or drop it so no client can render from raw deltas? (Leaning drop — the `transcript` state makes raw deltas a foot-gun.)
- **Q-N10** Switch = in-process agent-scope reboot vs client respawn (§6). Both must behave identically; likely respawn first (lobby makes it cheap/litter-free). Decide in Stage 0.
- **Q-N11** `Line` entry-id stability for the client's render diffing (the client, not the wire, diffs `transcript` pushes) — confirm by reading `accumulator.ts` at Stage 1.
