# zc UI Protocol Specification (draft 2 — seam model)

**Status**: draft for review · **Base**: letta-code 0.28.9 (`d253471e`) · **Ground truth**: [TOUCHPOINTS.md](./TOUCHPOINTS.md) (117 touchpoints + 64 commands — the rows of this spec ARE the seam)

> Draft 1 specced a capability-negotiated superset of the app-server `protocol_v2` implemented as listener patches. Draft 2 replaced that with a serialization seam cut through the native TUI application via headless React. **Draft 3 supersedes the cut mechanism**: the fork *adds a protocol mode* (`letta ui-server`) alongside the untouched Ink TUI, built the way `headless.ts` is built — the seam definition and every frame in this spec are unchanged; only how the server side is constructed changed (R13). The app-server/listener remains untouched (Desktop keeps using it; irrelevant here).

## 1. Overview & goals

This document specifies the protocol produced by serializing the seam between the **native letta-code TUI application's rendering layer and its implementation**. Any TUI can be built on it — ZeptoCodeCLI's Go client first; the intended endgame is upstream's own Ink UI converted into just another client of the same seam.

**Governing principle (authoritative for every design decision in this document):**

> *The client renders; everything else is constructed state served over the protocol.*

Corollaries:

1. **Behavioral totality**: every TUI implementing the full spec behaves **exactly** the same. TUIs may build extra features *on top of* the protocol, but default protocol behavior is total — there is no per-client semantics. zc's custom Go command implementations are deleted; `/usage`, ExitStats, queue defer, bash mode, pickers, etc. are *native behavior, serialized* — never reimplemented.
2. **No capability negotiation, no grants**: the server process runs the same implementation modules as the native client and declares the native TUI's capability set (`TUI_MOD_CAPABILITIES` — fork-owned constants). Panels, all 11 hook events, all 64 commands, and full mod events are native by declaration and by shared call sites. Nothing is "granted"; there is nothing to degrade to.
3. **The client is dumb**: it never knows file paths, file formats, settings scopes, merge orders, or policy. It receives constructed state and sends semantic events.
4. **Not a protocol_v2 superset**: `protocol_v2` frame shapes are **borrowed opportunistically** where the seam happens to match (convenience for zc's existing Go wire code — §4 lists each borrow explicitly). There is no compat requirement; Desktop talks to the untouched app-server and never sees this protocol.

## 2. Architecture: the seam, served by an additive protocol mode

### 2.1 The fork

Fork = upstream letta-code at the installed release tag (0.28.9 / `d253471e`) **plus one additive mode**. The Ink TUI is untouched — it keeps calling core logic APIs directly, exactly as upstream ships it. The only permitted edits to existing files are **behavior-preserving extractions**: relocating React-bound policy into plain shared modules that both the Ink TUI and the protocol mode consume (upstream-quality refactors; code motion, no behavior change). Maintenance/rebase strategy is explicitly deferred (decision 2026-07-16: "just get something working").

### 2.2 Server construction: additive protocol mode (R13, supersedes headless React)

A new entry point (`letta ui-server`) is built the way `headless.ts` is built — a React-free host over the same implementation modules. The in-tree precedents carry the load:

- **Headless bidirectional mode already proves the shape**: `letta -p --input-format stream-json` runs a long-lived stdio JSON-lines loop over the same `sendMessageStream`/`getBackend` path as the TUI, with interactive approvals (`control_request`/`can_use_tool`), interrupts, and queueing (`headless.ts:958, 3644, 4089`). `ui-server` is that pattern, emitting seam frames instead of Claude-SDK frames.
- **The policy layer is already shared, not React-bound**: `headless.ts` imports `cli/helpers/accumulator` (the transcript-construction engine, 1.6k lines, zero React), `context-tracker`, `approval-classification`, `memory-reminder`, `reflection-launcher`, and the full `agent/*` surface. The command registry is likewise plain. The protocol mode imports the same modules — behavior identity with the Ink TUI by *shared implementation*, not by parallel reimplementation.
- **Fork-only privilege**: the mode constructs its mod adapter with the native TUI capability set (`TUI_MOD_CAPABILITIES` — panels, commands, all events). Capabilities are per-adapter constants; in the fork they are ours to declare.
- **Input arrives as semantic events.** The client owns text *editing* (terminal truth); the server owns text *interpretation* — submit routing (message vs `/command` vs `!` bash), paste-placeholder resolution, queue defer policy.

### 2.3 Share, don't duplicate (the drift rule)

With an additive mode, protocol clients are identical *to each other* by construction; identity with the native Ink TUI holds exactly where implementation is shared. Therefore, authoritative rule for every server-side behavior:

1. **Already a plain module** (accumulator, registry, context-tracker, settings-manager, hooks executor, mod engine, stats, notifications logic, …) → import it.
2. **React-bound** (submit routing in `use-submit-handler.ts`, bash handlers in `use-bash-handlers.ts`, paste registry, defer state, loop state-machine details, hook call sites for `Stop`/`UserPromptSubmit`/`SessionStart`/`SessionEnd`/`Notification`, mod lifecycle event emission) → **extract** into a shared module consumed by both the Ink hook and the protocol mode. Never reimplement in parallel.
3. Residual divergence from the Ink TUI is a tracked defect, not a variant.

### 2.4 Consequences (vs the draft-1 listener patches)

| Draft-1 patch | Additive-mode status |
|---|---|
| Listener `panels:true` + capability derivation | not needed — the mode's adapter declares `TUI_MOD_CAPABILITIES` |
| Port Stop/UserPromptSubmit/SessionStart into listener (#1282) | host invokes the shared hook call-site helpers at the same loop points as the Ink client (extraction per §2.3.2 — small, and upstreamable as the #1282 fix) |
| Enable lifecycle/compact/llm listener mod events | mode's capability set enables them; host emits lifecycle events at extracted call sites |
| Lift `SUPPORTED_REMOTE_COMMANDS` 11-command cap | not applicable — the mode uses the full native registry directly |
| Reminders/auto-compaction/interrupt-cascade listener parity checklist | shared modules (`headless.ts` already exercises them) |

What remains is serialization work (emitters over shared-module output, semantic input events, request/response frames) plus the §2.3.2 extractions.

## 3. Conventions

- **Framing**: JSON frames, `type` discriminator, `request_id` correlation for request/response pairs, push frames uncorrelated. (Same conventions as protocol_v2 — deliberate, so zc's Go framing layer survives.)
- **Naming**: plain native-style frame names. No `ext_`/`zc_` prefixes — this is a new protocol at a seam upstream never serialized; there is nothing to collide with (resolved decision R7).
- **Versioning**: `hello` handshake (§5.0) carries `protocol_version` (integer, starts at 1) and `runtime_build` (fork identifier). Unknown frames MUST be ignored by both sides.
- **Transport (resolved R9)**: **stdio** — the client spawns the server as a child process; JSON-lines frames over stdin/stdout, logs on stderr. Works like current local mode: each TUI gets its own backend instance, no ports, no daemons, server dies with the client. **Stdout hygiene is a hard rule**: protocol frames own stdout exclusively; anything harness code prints must be shunted to stderr (in-tree precedent: headless `--output-format stream-json`). One client per server process *by construction*.

## 4. Borrowed vocabulary (protocol_v2 shapes reused opportunistically)

Each row is a deliberate borrow: the seam data matches an existing `protocol_v2` shape closely enough that redefining it would be gratuitous. The TS shapes in `src/types/protocol_v2.ts` are normative *for shape only*; semantics are sourced from the TUI implementation below the seam, not from the listener.

| Borrowed frame(s) | Seam usage | Borrow notes |
|---|---|---|
| `input {create_message}` | semantic submit of a plain message (§5.2 routes other submits) | image content parts inherited; `override_model` field added (§5.2) |
| `input {approval_response}` | approval decisions incl. `selected_permission_suggestion_ids`, `updated_input` | unchanged; AskUserQuestion rides `updated_input` as before |
| `control_request {can_use_tool}` | approval dialog descriptor (args, suggestions, `blocked_path`, `DiffPreview`) | exactly the props `ApprovalDialog` receives — already a seam-shaped frame |
| `update_loop_status` | 8-state loop status + `executing_tool_call_ids` | unchanged |
| `update_queue` | constructed queue state (typed kinds/sources) | plus `deferred` field (§5.3) |
| `update_subagent_state` | subagent snapshots | unchanged |
| `abort_message`/`abort_message_response` | esc-interrupt semantic event | subagent cascade is native below the seam |
| `device_status` | ambient constructed state (mode, cwd, git_context, toolset, background_processes, pending_control_requests, experiments, should_doctor, memory_directory, reflection settings) | gains `command_catalog` (§5.6); `current_available_skills` becomes honest (§5.10); `letta_code_version` populated (native `getVersion`) |
| `change_device_state` | cwd + permission-mode semantic events | unchanged |
| `sync` | ~~reconnect state replay~~ likely unnecessary under stdio (no reconnect exists; R9) | startup/resume state arrives via `hello_response` + `transcript_sync` (§5.1); drop unless Stage 1 finds a need |
| memory suite (`list_memory {include_references}`, `read/write/delete_memory_file`, `memory_history`, `memory_file_at_ref`, `memory_commit_diff`, `enable_memfs`, `memory_updated`) | /memory viewers' data | unchanged |
| `list_models`, `update_model`, `update_toolset` | model catalog + persistent-scope switching | picker *presentation* is an overlay (§5.5); these carry the data/actions |
| `list_connect_providers`, `connect_provider`, `disconnect_provider` | /connect data/actions (API-key methods) | OAuth = native browser flow, out of scope as before |
| agents/conversations suite (`agent_list/retrieve/update/delete`, `create_agent` w/ presets, `conversation_list/retrieve/create/update/recompile/fork {hidden}/messages_list/compact`) | picker data, /new /fork /btw /title /rename /description | `/btw` = fork{hidden}+input, pane state via §5.1 entry kinds |
| `cron_*` (8) + `crons_updated` | /crons | unchanged |
| `skill_enable/disable` + `skills_updated` | /skills actions | listing via §5.10 |
| `get/set_reflection_settings`, `get_experiments`/`set_experiment` | /sleeptime /experiments | unchanged |
| `search_branches`, `checkout_branch` | branch picker | unchanged |
| `secret_list`, `secret_apply` | /secret | plaintext caveat unchanged |
| files suite (`search_files`, `grep_in_files`, `list_in_directory`, `get_tree`, `read_file`, `write_file`, `edit_file`, `watch_file`/`file_changed`) | @-completion data (`search_files`), find-in-files, file viewers | clients MUST NOT use `write_file`/`edit_file` to bypass dedicated CRUD frames (R4) |
| `execute_command` + `slash_command_start/end` | run any catalog command (§5.6) | full native registry; interactive commands drive overlays (§5.5) |
| `external_tool_call_request/response` | controller-owned tools | unchanged |
| terminal PTY (`terminal_spawn/input/resize/kill/spawned/output/exited`) | optional embedded terminal surface | NOT the bash-mode route (R3 routes `!` through submit) |
| `stream_delta` | **optional debug/advanced channel only** | rendering MUST be driven by §5.1 transcript frames, or clients diverge (accumulation is policy) |

## 5. Seam frames

Format per frame: direction (`→` client→server, `←` server→client), shape, semantics, TOUCHPOINTS coverage.

### 5.0 Handshake

```ts
/** → first frame after connect. */
interface HelloCommand {
  type: "hello"; request_id: string;
  protocol_version: number;              // highest version client speaks
  client: { name: string; version: string };
}
/** ← response. */
interface HelloResponseMessage {
  type: "hello_response"; request_id: string;
  protocol_version: number;              // version server will speak (min of the two)
  runtime_build: string;                 // e.g. "zc-fork-0.28.9+s1"
  letta_code_version: string;
}
```

### 5.1 Transcript (covers 1.2, 1.3, 1.8 rendering side, 1.13, 1.15, 5.2, 9.1 output, 2.4 post-hoc diffs)

The single most important seam surface. The accumulator (`helpers/accumulator.ts`) — which turns raw stream chunks into the `Line` model the transcript components render — is **implementation**, and runs below the seam. Clients receive constructed transcript entries; they never re-accumulate raw deltas. This is what makes N clients behave identically.

```ts
/** ← full snapshot: on conversation open, on reconnect, on request. */
interface TranscriptSyncMessage {
  type: "transcript_sync";
  conversation_id: string;
  entries: TranscriptEntry[];
  revision: number;                      // monotonic per conversation
}

/** ← incremental: upserts keyed by entry id (append-ordered). */
interface TranscriptUpdateMessage {
  type: "transcript_update";
  conversation_id: string;
  revision: number;
  upserts?: TranscriptEntry[];           // new or fully-replaced entries
  append?: { entry_id: string; field: "text" | "reasoning"; delta: string }; // hot streaming path
  remove?: string[];                     // entry ids (rare: optimistic rollback)
}

interface TranscriptEntry {
  id: string;
  turn_id?: string;
  subagent_id?: string;                  // nested rendering; client draws tree
  kind: "user" | "assistant" | "reasoning" | "tool_call" | "shell" | "event" | "error" | "question";
  phase: "streaming" | "ready" | "running" | "finished" | "canceled" | "failed";
  text?: string;                         // markdown for user/assistant; raw for shell
  tool?: {
    name: string; call_id: string;
    args_summary: string;                // server-constructed header params
    args?: unknown;                      // full args (client may hide behind expand)
    result_preview?: string;             // server-truncated preview
    result_full_available: boolean;      // fetch via transcript_entry_detail
    diffs?: DiffPreview;                 // Edit/MultiEdit: persisted past approval (borrowed shape)
    status: "pending" | "approved" | "denied" | "success" | "error";
  };
  shell?: { command: string; exit_code?: number; cwd: string };   // bash-mode entries
  event?: { label: string; detail?: string };                      // compaction, mode changes, etc.
  usage?: { input_tokens?: number; output_tokens?: number; cost_usd?: number }; // turn footers
  created_at: string;
}

/** → fetch untruncated payloads (full tool results, full args). */
interface TranscriptEntryDetailCommand { type: "transcript_entry_detail"; request_id: string; conversation_id: string; entry_id: string; }
interface TranscriptEntryDetailResponseMessage {
  type: "transcript_entry_detail_response"; request_id: string;
  entry: TranscriptEntry;                // with full result/args inline
}
```

- Truncation policy (previews vs full) is server-side and identical for every client; clients render what they get and may *pull* full detail — they never truncate at ingest.
- `question` entries + `control_request` cover AskUserQuestion: dialog via approval frames, transcript record via entry (native `chat/question` parity).

### 5.2 Semantic input (covers 9.1, 9.2, 1.1; resolves Q3 server-side)

```ts
/** → the submit event. The server routes; the client never interprets. */
interface InputSubmitCommand {
  type: "input_submit"; request_id: string;
  conversation_id: string;
  text: string;                          // verbatim buffer, placeholders unresolved
  attachments?: { image_ref: string }[]; // from input_paste image registration
  override_model?: { model_id?: string; model_handle?: string }; // per-turn (TOUCHPOINTS 1.1)
}
interface InputSubmitResponseMessage {
  type: "input_submit_response"; request_id: string;
  routed: "message" | "command" | "bash" | "rejected";
  error?: string;                        // e.g. UserPromptSubmit hook denial reason
}
```

- Routing is native: `/x` → command registry; leading `!` → bash mode (`spawnCommand` + alias expansion below the seam; output becomes a `shell` transcript entry; `<bash-input>/<bash-output>` system-reminder injection on next message happens server-side); otherwise message send. `UserPromptSubmit` hooks run natively before dispatch.
- Plain-message submits MAY alternatively use borrowed `input {create_message}`; `input_submit` is the total route and what full-parity clients use.

```ts
/** → register pasted content; display placeholder comes back. Paste registry lives below the seam. */
interface InputPasteCommand {
  type: "input_paste"; request_id: string;
  content_kind: "text" | "image";
  text?: string;                         // large text paste
  image_base64?: string;                 // clipboard image (server resizes: sharp/imagemagick)
}
interface InputPasteResponseMessage {
  type: "input_paste_response"; request_id: string;
  placeholder: string;                   // "[Pasted text #1 +120 lines]" | "[Image #1]"
  image_ref?: string;                    // for input_submit.attachments
}
```

- The client inserts the placeholder into its edit buffer verbatim; resolution on submit is server-side (`resolvePlaceholders` parity). Client-side prompt styling on a leading `!` is permitted (pure presentation); behavior stays server-routed.

### 5.3 Queue defer (covers 4.4; resolves Q6 server-side)

```ts
/** → toggle. State is server-held; survives client restarts; identical across clients. */
interface QueueDeferSetCommand { type: "queue_defer_set"; request_id: string; deferred: boolean; }
interface QueueDeferSetResponseMessage { type: "queue_defer_set_response"; request_id: string; deferred: boolean; }
```

- `update_queue` (borrowed) gains `deferred: boolean` so all clients render the `○` vs `>` state identically. Flush-on-untoggle is native policy below the seam.

### 5.4 Mod panels — client-polled (covers 6.2, 6.3, 6.9; resolves Q1)

Panels are **polled, not pushed**: native semantics are render-on-every-Ink-frame (renders are pure functions), so a client-controlled poll is the honest serialization — the client knows its own cadence and visibility; the server keeps no throttle machinery and no width state.

```ts
/** → evaluate all open panels at the given width. */
interface ModPanelsQueryCommand { type: "mod_panels_query"; request_id: string; width: number; }
/** ← evaluated panels. */
interface ModPanelsResponseMessage {
  type: "mod_panels_response"; request_id: string;
  panels: {
    id: string;                          // namespaced mod panel key
    owner: string;                       // mod id
    order: number;                       // <0 below input · 0 primary statusline · 1 product-status · >1 above input
    lines: string[];                     // evaluated render output (ANSI allowed; no cursor movement honored)
  }[];
}
```

- Evaluation uses the same `render(ctx)` context construction as `ModPanelRow.tsx:48`, running in the native adapter (`TUI_MOD_CAPABILITIES`) below the seam.
- Suggested client cadence: 500–1000 ms while panels visible; on resize; paused while hidden. Cadence is a client concern by design.

```ts
/** → registry + diagnostics for /mods. */
interface ModRegistryCommand { type: "mod_registry"; request_id: string; }
interface ModRegistryResponseMessage {
  type: "mod_registry_response"; request_id: string;
  mods: { id: string; source: "local" | "package"; path: string;
          capabilities: { tools: string[]; commands: string[]; panels: string[]; events: string[]; providers: string[] } }[];
  diagnostics: { mod: string; phase: string; severity: "error" | "warning"; message: string; at: number }[];
}
/** ← push when the registry changes (reload, package install). Client responds by re-querying. */
interface ModsUpdatedMessage { type: "mods_updated"; }
```

### 5.5 Overlays & pickers (covers 8 interactive commands, 11.1–11.10; the "same behavior in every TUI" mechanism)

Interactive commands (`/model`, `/agents`, `/skills`, `/hooks`, `/subagents`, `/memory`, …) open selector components in the native UI. Below the seam their **data + state logic** runs unchanged; the components' props/events are serialized as overlay descriptors. Clients render the descriptor however fits their toolkit — contents, ordering, filtering behavior, and outcomes are identical everywhere.

```ts
/** ← current overlay stack (top = active). Pushed on every overlay state change. */
interface OverlayStateMessage {
  type: "overlay_state";
  stack: OverlayDescriptor[];
}
interface OverlayDescriptor {
  overlay_id: string;
  kind: "select" | "multiselect" | "form" | "confirm" | "viewer" | "pager";
  title: string;
  items?: { id: string; label: string; description?: string; badge?: string; disabled?: boolean; selected?: boolean }[];
  fields?: { id: string; kind: "input" | "password" | "text" | "select" | "confirm"; label: string; value?: string; options?: string[] }[];
  body?: { format: "markdown" | "diff" | "plain"; content: string };  // viewer/pager
  footer_hint?: string;
}

/** → client interaction with the active overlay. */
interface OverlayEventCommand {
  type: "overlay_event"; request_id: string;
  overlay_id: string;
  event: "select" | "submit" | "cancel";
  item_id?: string;                      // select
  values?: Record<string, string>;       // form submit
}
interface OverlayEventResponseMessage { type: "overlay_event_response"; request_id: string; ok: boolean; error?: string; }
```

- Filtering-as-you-type inside a picker is client-side *presentation* over the served items (fuzzy match on `label`); pagination/search that requires data access is served (descriptor refresh).
- This replaces every zc-custom picker implementation. zc's Go pickers become one generic overlay renderer.

### 5.6 Command catalog (covers 8 routing metadata, 9.5, 11.9, custom commands)

```ts
/** [field] on borrowed device_status */
command_catalog?: CommandInfo[];

interface CommandInfo {
  id: string;                            // "search", "audit" (mod), "review" (custom)
  description: string;
  args_hint?: string;                    // "<query>"
  routing: "interactive" | "non_state" | "queued";  // command-routing.ts classes, verbatim
  hidden?: boolean;
  source: "builtin" | "custom" | "mod";
  namespace?: string;                    // custom-command subdirectory namespacing
}
```

- The catalog is the full native registry + custom `.commands/*.md` (project + user, project-shadows-user — scanned below the seam at the *server's* cwd) + mod commands. `supported_commands`/`mod_commands` on `device_status` remain as legacy fields.
- Execution: borrowed `execute_command` for any catalog id. Interactive commands manifest as §5.5 overlays; output rides `slash_command_start/end` + transcript entries. There is no restricted set: the registry below the seam is the native one.

### 5.7 Settings — fully-constructed state (covers 10.1; commands /pin /unpin /pinned /profiles /reasoning-tab /memfs aliases; resolves Q2)

Scopes (global/project/projectLocal), merge order, and file locations are **server-internal and invisible**. The client reads one effective settings object and writes semantic patches; the server routes each key to its canonical location (policy below the seam). The runtime is the only writer — the external-write cache race is dead.

```ts
interface SettingsReadCommand { type: "settings_read"; request_id: string; }
interface SettingsReadResponseMessage {
  type: "settings_read_response"; request_id: string;
  settings: Record<string, unknown>;     // effective (merged) view, secrets redacted
  revision: number;                      // monotonic
}
interface SettingsPatchCommand {
  type: "settings_patch"; request_id: string;
  patch: Record<string, unknown>;        // RFC 7386 merge patch against the effective view
  expected_revision?: number;            // optimistic concurrency; mismatch → error, re-read
}
interface SettingsPatchResponseMessage { type: "settings_patch_response"; request_id: string; ok: boolean; revision?: number; error?: string; }
/** ← push on any settings change (any writer). */
interface SettingsUpdatedMessage { type: "settings_updated"; revision: number; }
```

- Pins, profiles, reasoning-tab, memfs aliases, recents = `settings_patch` calls; no per-feature frames.
- Secret-bearing keys: redacted on read, rejected on patch (`secret_*` frames remain the explicit path).

### 5.8 Hooks: lifecycle signals, notifications, config CRUD (covers 7.4, 7.5, 7.10)

`Stop`, `UserPromptSubmit`, `SessionStart` fire **natively** below the seam (their call sites are the loop itself — no port, no patch). Two events depend on client lifecycle, which only the client knows:

```ts
/** → client lifecycle transitions. Fire-and-forget. */
interface SessionEventCommand {
  type: "session_event";
  event: "session_start" | "session_end" | "conversation_open" | "conversation_close";
  duration_ms?: number;                  // session_end
}
```

- `session_end` runs SessionEnd hooks (`AppCoordinator.tsx:1164` parity — same code, same process). `conversation_open/close` drive mod lifecycle events. The server also treats client disconnect as best-effort `session_end` so hooks don't silently skip on crash.

```ts
/** ← server-side notification triggers; the client only emits terminal signals (bell/OSC). */
interface NotificationMessage {
  type: "notification";
  level: "info" | "warning" | "error";
  message: string;
  reason: "awaiting_approval" | "awaiting_input" | "turn_complete" | "hook" | "other";
}
```

- Emitted where `app/notifications.ts` fires today (same code below the seam); **Notification hooks run at emit time, server-side**. Client-side focus policy (only-when-unfocused) is presentation.

Config CRUD — dedicated frames (resolves Q4); the client never sees the four settings files or merge order:

```ts
interface HooksListCommand { type: "hooks_list"; request_id: string; }
interface HooksListResponseMessage {
  type: "hooks_list_response"; request_id: string;
  hooks: { event: string; matcher?: string; kind: "command" | "prompt"; spec: unknown; source_label: string; editable: boolean }[];
}
interface HooksUpdateCommand {
  type: "hooks_update"; request_id: string;
  action: "add" | "remove" | "replace";
  hook: { event: string; matcher?: string; kind: "command" | "prompt"; spec: unknown };
  target_id?: string;                    // for remove/replace, from hooks_list ordering ids
}
interface HooksUpdateResponseMessage { type: "hooks_update_response"; request_id: string; ok: boolean; error?: string; }
```

### 5.9 Message search (covers 8 /search, 11.6)

```ts
interface SearchMessagesCommand {
  type: "search_messages"; request_id: string;
  query: string;
  scope: { agent_id?: string } | "all";
  limit?: number;                        // default 50
  cursor?: string;
}
interface SearchMessagesResponseMessage {
  type: "search_messages_response"; request_id: string;
  hits: { agent_id: string; conversation_id: string; message_id: string;
          role: "user" | "assistant" | "tool" | "system"; snippet: string; timestamp: string }[];
  next_cursor?: string;
}
```

Backed by `@/backend/message-search` (native /search implementation). zc's jsonl scanner retires.

### 5.10 Skills listing (covers 11.4)

```ts
interface SkillsListCommand { type: "skills_list"; request_id: string; }
interface SkillsListResponseMessage {
  type: "skills_list_response"; request_id: string;
  skills: { name: string; description: string; source: "bundled" | "global" | "agent" | "project"; enabled: boolean }[];
}
```

- Native `@/agent/skills` scan below the seam; `device_status.current_available_skills` becomes honest as a drive-by. `skills_updated` push (borrowed) signals re-query. Paths are deliberately not exposed (client is dumb); the /skills overlay (§5.5) handles interaction.

### 5.11 Subagent definitions (covers 5.5; resolves Q4 dedicated)

```ts
interface SubagentDefsListCommand { type: "subagent_defs_list"; request_id: string; }
interface SubagentDefsListResponseMessage {
  type: "subagent_defs_list_response"; request_id: string;
  defs: { name: string; description: string; source: "project" | "agent" | "bundled"; body: string; editable: boolean }[];
}
interface SubagentDefsWriteCommand { type: "subagent_defs_write"; request_id: string; name: string; body: string; location: "project" | "agent"; }
interface SubagentDefsWriteResponseMessage { type: "subagent_defs_write_response"; request_id: string; ok: boolean; error?: string; }
interface SubagentDefsDeleteCommand { type: "subagent_defs_delete"; request_id: string; name: string; }
interface SubagentDefsDeleteResponseMessage { type: "subagent_defs_delete_response"; request_id: string; ok: boolean; error?: string; }
```

Schema validation + multi-location resolution native below the seam.

### 5.12 Reflection launch (covers 8 /reflect)

```ts
interface StartReflectionCommand { type: "start_reflection"; request_id: string; agent_id: string; from_conversation_id?: string; }
interface StartReflectionResponseMessage { type: "start_reflection_response"; request_id: string; started: boolean; background_process_id?: string; error?: string; }
```

Runs the native `reflection-launcher.ts` path (worktrees, subagent spawn); progress via borrowed `device_status.background_processes`; results via native queue injection. zc's `letta dream` shell-out retires.

### 5.13 Memory repository ops (covers 8 /memory-repository)

```ts
interface MemoryRepositoryCommand { type: "memory_repository"; request_id: string; action: "status" | "set" | "unset" | "push"; url?: string; }
interface MemoryRepositoryResponseMessage {
  type: "memory_repository_response"; request_id: string; ok: boolean;
  status?: { url: string | null; last_push: string | null; ahead: number };
  error?: string;
}
```

### 5.14 Session stats (covers 10.6, 13.3; resolves Q5: native semantics, serialized)

```ts
interface SessionStatsCommand { type: "session_stats"; request_id: string; }
interface SessionStatsResponseMessage {
  type: "session_stats_response"; request_id: string;
  since: string;
  turns: number;
  tokens: { input: number; output: number; cache_read?: number; cache_write?: number };
  cost_usd?: number;
}
```

- The window is whatever native `SessionStats` (`@/agent/stats`) accumulates — the same object ExitStats reads, serialized. Not a protocol-chosen window: native behavior is the spec. Absent fields are absent (local-backend sparsity), never fabricated.

### 5.15 Context window overview (covers 10.4; /context)

```ts
interface ContextWindowOverviewCommand { type: "context_window_overview"; request_id: string; }
interface ContextWindowOverviewResponseMessage {
  type: "context_window_overview_response"; request_id: string;
  max_tokens: number; current_tokens: number;
  breakdown: { system: number; core_memory: number; summary_memory: number; functions: number; messages: number; external_memory?: number };
}
```

Wraps the native `context-chart.ts` data source. Per-turn token *history* for the chart: served from the native `context-tracker` state (same seam surface), included in the response as `history?: { turn: number; tokens: number; compaction?: boolean }[]`.

### 5.16 Conversation change push (covers 10.15)

```ts
interface ConversationsUpdatedMessage {
  type: "conversations_updated";
  agent_id: string;
  conversation_id?: string;
  reason: "created" | "head_moved" | "title" | "archived" | "deleted";
}
```

Powers the native conversation-switch alert + live picker refresh (mirrors `memory/skills/crons_updated`).

### 5.17 Agent export (covers 8 /download)

```ts
interface AgentExportCommand { type: "agent_export"; request_id: string; agent_id: string; dest_path?: string; }
interface AgentExportResponseMessage { type: "agent_export_response"; request_id: string; ok: boolean; path?: string; error?: string; }
```

### 5.18 Exit stats (covers 13.3)

```ts
/** → request on quit; client renders the farewell screen from data. */
interface ExitStatsCommand { type: "exit_stats"; request_id: string; }
interface ExitStatsResponseMessage {
  type: "exit_stats_response"; request_id: string;
  duration_ms: number; turns: number;
  tokens: { input: number; output: number }; cost_usd?: number;
  agent_name: string; pin_hint: boolean;
}
```

Same snapshot native `ExitStats.tsx` receives. (Client sends `session_event {session_end}` after rendering.)

## 6. Decisions for remaining TOUCHPOINTS gaps (no new frames)

| Gap | Decision |
|---|---|
| Bash mode (`!`) (9.1) | **Server-side via `input_submit` routing** (§5.2). Native `use-bash-handlers` semantics below the seam: exec, output caching, system-reminder injection. Client renders `shell` transcript entries and may style the prompt on a leading `!` (presentation only). PTY frames are NOT the `!` route. |
| Paste registry (9.2) | **Below the seam** via `input_paste` (§5.2). Client edit buffer holds placeholders verbatim. |
| Image paste (9.3) | `input_paste {image_base64}` → server resize (sharp/imagemagick, native code) → `image_ref` attached on submit. Terminal/clipboard capture is client (it is the terminal). |
| Task-notification parsing (4.5) | Server-side: queue items arrive in `update_queue` already typed; `<task-notification>` parsing for display joins the transcript/event construction below the seam. No client parser. |
| Token smoothing (1.14) | Client presentation (pacing of `append` deltas). Explicitly allowed to differ per client — it is rendering, not behavior. |
| Window title (13.2) | Client composes from served state (`update_loop_status`, agent/conversation info). Presentation. |
| Bell/OSC emission (13.1) | Client emits on `notification` frames; focus policy client-side. Hook execution server-side (§5.8). |
| Release notes (10.10) | Server-side: native `release-notes.ts` runs below the seam; shown-once state in settings; delivered as an `event` transcript entry or §5.5 viewer overlay. |
| /feedback, /palace, /ade | Overlay descriptor + server executes (HTTP POST / returns URL for the client to open). Browser-opening is client (it owns the desktop session); URL construction is server. |
| /terminal keybinding installer, /install-github-app, /login, /logout | Out of scope (client-environment / cloud / native-CLI browser flows), unchanged from draft 1. |
| /mcp | Out of scope at 0.28.9 (no local-backend support upstream). |
| /link, /unlink | Deprecated upstream; skip. |
| OAuth provider connect (11.8) | Out of scope (native browser flow); API-key path via overlay + borrowed frames. |
| Channels | Frames exist in protocol_v2 if ever needed; zc defers the surface. Untouched. |
| Billing tier (10.11) | Cloud-only; out of scope locally. |

## 7. Client-side residue (deliberate, complete)

Anything not listed here and not served by §4/§5 is server-side. This list SHRANK from draft 1 — paste registry, defer state, bash exec, command metadata, and all picker logic moved below the seam.

- **Text editing**: textarea, cursor, wrapping, kill-ring, undo, input history recall, completion *UI* (data: `search_files`, `command_catalog`). The buffer's *interpretation* is server-side (§5.2).
- **Rendering**: markdown/glamour, diff presentation, tool-card layout, tree layout for subagent entries, spinners/animations, token display pacing, panel *placement* (lines arrive evaluated), overlay/dialog *presentation* (descriptors arrive constructed), ExitStats farewell screen (data served).
- **Terminal integration**: keybindings, focus tracking, clipboard read/write (paste *content* goes to `input_paste`), bell/OSC emission policy, window-title composition, terminal capability probing, resize handling.
- **Client debug**: frame dumps (ZC_DEBUG_FRAMES-style), client logs.

## 8. Security & privacy notes

- `settings_read` redacts secret-bearing keys; `secret_list` remains the explicit plaintext path (existing behavior).
- `mod_panels_response.lines` may contain mod-supplied ANSI; clients sanitize (SGR only; no cursor movement honored).
- `hooks_update` writes config only; hook *execution* authorization is native semantics, unchanged.
- Single client per server process by construction (stdio child, R9); the server serializes all state writes (settings, hooks, defs) internally.
- `input_paste` ships clipboard content over a private pipe to a child process owned by the same user; zero network exposure.

## Appendix A — Staged implementation plan

Fork: `letta-ai/letta-code` @ the installed release tag (0.28.9 / `d253471e`). Each stage: bun build → zc tmux gauntlet against the probe agent → stage commit. Maintenance/rebase strategy deferred by decision (2026-07-16).

**Stage 0 — protocol-mode skeleton**
- New entry point (`src/zc/ui-server.ts`, wired as `letta ui-server`): stdio JSON-lines host modeled on `runBidirectionalMode` (`headless.ts:3644`) — stdout-hygiene guard first, `hello` handshake, backend + agent/conversation boot, mod engine constructed with `TUI_MOD_CAPABILITIES`.
- Drive one full turn: inject a message through the shared submit path; loop, tools, mods run; final assistant text emitted (raw, pre-transcript-frames).
- **Exit criterion**: mode boots on a clean stdout, a turn runs end-to-end with tool execution, mods load with full TUI capabilities (verify muscle-memory registers its panel).
- Risk retired: mode viability + mod-engine-with-TUI-capabilities outside React.

**Stage 1 — proving slice (seam serialization, 4 surfaces)**
- **Transcript emission**: `transcript_sync`/`transcript_update` (§5.1) emitted from the shared accumulator's output.
- **Input submit**: `input_submit` routed through the native submit path (message route at minimum; bash/command routes may land in stage 2).
- **Approval round-trip**: `control_request` emitted from the native approval flow; `input {approval_response}` injected back (borrowed shapes — zc's existing Go approval code applies nearly unchanged).
- **`mod_panels_query`** (§5.4): evaluate open panels at requested width via the native adapter.
- stdio host: JSON-lines framing on stdin/stdout, `hello` handshake, stdout-hygiene guard (redirect `console.*`/harness prints to stderr before anything else loads).
- **Exit criterion**: zc (Go) connects to the forked process, renders a live streamed turn with tool cards from transcript frames, answers an approval, and renders muscle-memory's panel via polling.

**Stage 2 — input policy + commands + overlays**
- `input_submit` full routing: bash mode (`shell` entries, reminder injection), slash commands via native registry.
- `device_status.command_catalog` (+ custom `.commands` scan) and `execute_command` for the full registry.
- Overlay serialization framework (§5.5): descriptor emission + `overlay_event` injection; convert the highest-traffic selectors first (model, agents, conversations, skills).
- `input_paste` + paste registry relocation; `queue_defer_set` + `update_queue.deferred`.

**Stage 3 — settings, hooks, notifications, lifecycle**
- `settings_read`/`settings_patch`/`settings_updated` over native settings-manager (redaction + revision).
- `session_event` (SessionEnd hooks, mod conversation lifecycle), `notification` emission + Notification hooks at native trigger points.
- `hooks_list`/`hooks_update` over native `hooks/writer.ts`.

**Stage 4 — feature surfaces**
- `search_messages` (native message-search), `skills_list` (+ honest `current_available_skills`), `context_window_overview` (+ history), `session_stats`, `exit_stats`, `conversations_updated`, `transcript_entry_detail`.

**Stage 5 — long tail**
- `subagent_defs_*`, `start_reflection`, `memory_repository`, `agent_export`, `mod_registry`/`mods_updated`, release-notes delivery, /feedback//palace overlay routes.
- Sweep TOUCHPOINTS for any row not yet exercised by a zc feature; close or re-classify each (exhaustive rule).

**zc (Go) demolition track** (parallel, per stage): delete the Go implementations each stage obsoletes — accumulator/ingest truncation (stage 1), client command registry + custom pickers (stage 2), settings/hooks/profile file access (stage 3), jsonl /search + `letta dream`/`node -e` shell-outs (stage 4/5). End state: client = editing + rendering + keybindings + terminal integration, exactly §7.

## Appendix B — Decisions & open questions

### Resolved (2026-07-16, Jack)

| # | Decision | Rationale |
|---|---|---|
| R1 | Panels are client-polled (`mod_panels_query {width}`) | Matches native render-on-every-frame semantics; client owns cadence/visibility; no server throttle machinery |
| R2 | Settings = fully-constructed merged state; scopes server-internal | Scope mechanics are policy, not rendering; kills the cache race by making the runtime sole writer |
| R3 | Bash mode server-side via submit routing | Every full-protocol TUI behaves exactly the same; client interprets nothing |
| R4 | Dedicated CRUD frames (hooks, subagent defs) over generic file I/O | Client never learns file paths/formats; server validates schemas |
| R5 | Session stats = native semantics serialized | The protocol serializes native behavior; it does not choose windows |
| R6 | Queue defer server-side | Native's client-held state relocates with the input-policy cut; identical behavior across TUIs |
| R7 | Plain native-style frame names | New protocol at a seam upstream never serialized; nothing to collide with |
| R8 | ~~Cut strategy = headless React ("Ink spoofing")~~ superseded by R13 | (kept for history) |
| R9 | Transport = stdio (JSON-lines over stdin/stdout, stderr logs) | Works like current local mode: one backend child per TUI, no ports/daemons, dies with the client; stdout hygiene rule applies |
| R10 | Process lifecycle = client-spawned per session | Follows from R9; matches native one-app-one-session assumptions below the seam (cron lease, SessionStart) |
| R11 | Multi-client attach = does not exist | Private pipe by construction; late-join/attach questions dissolve |
| R12 | Reconnect/replay = does not exist | No socket to drop; restart = fresh child resuming conversation state from storage (like relaunching native `letta`), `transcript_sync` on open covers replay; borrowed `sync` frame likely dropped |
| R13 | Server = **additive protocol mode** (`letta ui-server`), Ink TUI untouched | Headless bidirectional mode proves the React-free host shape; policy layer already lives in shared plain modules (`headless.ts` imports accumulator etc.); React-bound residue is *extracted* into shared modules per §2.3, never duplicated. Cleaner diff, no TTY/React hazards, strongest upstream story ("new mode" PR touches no UI code) |

### Open (new, raised by the seam model)

(Q-N1–Q-N4 resolved as R9–R12; numbering kept for traceability.)

5. ~~**Q-N5 Headless host mechanics**~~ dissolved by R13 (no React runs in the server; the host is a plain module like `headless.ts`).
6. **Q-N6 Overlay descriptor completeness**: are `select/multiselect/form/confirm/viewer/pager` sufficient for all native selectors (SkillsDialog nested actions, HooksManager multi-step flows, MemoryTabViewer tree)? Stage 2 converts the worst cases first and extends the descriptor vocabulary if needed (additive).
7. **Q-N7 `stream_delta` debug channel**: keep raw deltas available for client debugging/advanced use, or drop entirely to guarantee no client ever renders from them? (Spec keeps them optional-and-discouraged; confirm.)
