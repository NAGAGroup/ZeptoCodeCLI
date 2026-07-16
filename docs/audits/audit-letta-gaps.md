# Letta Code 0.28.8 → ZeptoCodeCLI Gap Audit

Deep source-level audit of `/tmp/letta-code-src` (v0.28.8, matches zc's pin) for functionality zc has NOT captured. Excludes shipped features and conscious scope-outs (/login, Channels UI, aliases, /mcp beyond stub, upstream mod panels).

Legend: **[WIRE]** = usable by a remote client over the app-server protocol · **[LOCAL]** = in-process only, needs shell-out / local-file access (zc's /search & /reflect pattern) · **[CLIENT]** = purely client-side behavior zc can implement itself.

---

## Top-10 shortlist (value ÷ effort)

1. **Pending-approval recovery** [WIRE] — `device_status.pending_control_requests` replays unanswered `can_use_tool` requests on connect/reconnect. If zc only builds the approval modal from live `control_request` frames, an approval pending at (re)connect time is invisible → turn hangs. Correctness bug-class fix; trivial effort. (`protocol_v2.ts:240-245, 455`)
2. **Git branch in statusline** [WIRE] — `device_status.git_context` carries `{branch, recent_branches[]}` (top 10 by recency, cwd-scoped). Free statusline upgrade; zc renders none of it. (`protocol_v2.ts:404-418`)
3. **`/btw` is wire-implementable TODAY** [WIRE] — native /btw = `conversation_fork` (body `{hidden: true}`) → `input` on the forked runtime scope → stream deltas filtered by conversation → ephemeral side pane (BtwPane.tsx: status idle/forking/streaming/complete, jump-to-conversation or dismiss). Runtime scopes are independent; the single-seat constraint is per control channel, not per turn — **no broker/jobs machinery needed**. zc deferred this on a wrong premise. (`components/BtwPane.tsx`, `protocol_v2.ts:1893`)
4. **Bash mode (`!`)** [CLIENT] — prompt flips to `!`, commands run locally (input locked while running, Ctrl+C interrupts, backspace-on-empty exits, shared history), outputs cached and injected as a `<bash-input>/<bash-output>` system-reminder prefix on the *next* message with "DO NOT respond unless asked" framing. zc runs on the same box as its app-server so this is straightforward — but execute in the conversation's cwd (`device_status.current_working_directory`). (`InputRich.tsx:1010-1660`, `app/use-bash-handlers.ts`, `use-submit-handler.ts:3876-3888`)
5. **Queue editing + defer** [WIRE+CLIENT] — `remove_queue_item` exists precisely for "load queued msg back into input" UX (desktop uses it); `update_queue` snapshots carry `kind` (message/task_notification/cron_prompt/…) and `source` (user/cron/subagent/channel) zc could badge. Native adds **Ctrl+D defer toggle** (hold queued user msgs instead of auto-flushing; `○` vs `>` bullet). (`protocol_v2.ts:2610-2622, 500-533`; `InputRich.tsx:1366-1373`, `QueuedMessages.tsx`)
6. **Richer turn status** [WIRE] — zc likely under-uses: `update_loop_status` has 8 states (incl. `RETRYING_API_REQUEST`, `WAITING_ON_APPROVAL`, `WAITING_ON_INPUT`) plus `executing_tool_call_ids` (explicitly designed as a self-healing alternative to pairing client_tool_start/end — comment says lifecycle events are unrecoverable if a frame drops); stream deltas `retry` (attempt/max_attempts/delay_ms → countdown line), `loop_error` (`is_terminal` flag distinguishes fatal vs recoverable), `status` (level info/success/warning). Cheap, big "feel" win. (`protocol_v2.ts:487-556, 609-630`)
7. **Custom slash commands** [CLIENT] — `.commands/*.md` (project) + `~/.letta/commands/*.md` (user), frontmatter `description`/`argument-hint`, subdirectory namespacing, project-shadows-user collision handling; body becomes the prompt. zc already has the pattern (skills-in-palette); this is Jack-alignable (dotfiles-style personal commands). (`cli/commands/custom.ts`)
8. **Image paste + large-paste placeholders** [CLIENT+WIRE] — Ctrl+V clipboard image import → `[Image #N]` placeholder → resized (sharp, imagemagick fallback) → sent as image content part in `MessageCreate` (wire input path normalizes images; strict failure mode for direct clients). Large text pastes (>5 lines or >500 chars) become `[Pasted text #N +X lines]` with display-value vs actual-value separation. (`helpers/clipboard.ts`, `PasteAwareTextInput.tsx`, `utils/image-resize.ts`, `listener/image-policy.ts`)
9. **TodoWrite/Plan rendering + trajectory summaries** [WIRE] — native renders TodoWrite args as a live checkbox list (`☒/☐`, in-progress highlighted; TodoRenderer.tsx), plans via PlanRenderer, and collapsed **TrajectorySummary** lines ("Worked for 3m · 12 steps"). All from data zc already receives in stream deltas. Also `enter_worktree` tool results get a dedicated renderer. (`TodoRenderer.tsx`, `PlanRenderer.tsx`, `TrajectorySummary.tsx`)
10. **Reconnect via `sync`** [WIRE] — lightweight state replay: `{type:"sync", recover_approvals, force_device_status, request_id}` → replays device_status/loop/queue + probes stale approvals, ack'd by `sync_response`. zc's auto-respawn path re-runs full `runtime_start`; for socket-drop-with-live-server, `sync` is the designed recovery. Also `abort_message` accepts `request_id` → `abort_message_response{aborted}` for deterministic esc-interrupt feedback. (`protocol_v2.ts:772-826, 928-944`)

---

## 1. Wire protocol surface

### Full inbound command inventory (protocol_v2.ts `WsProtocolCommand`)
input (create_message | approval_response), change_device_state, abort_message, sync, runtime_start, execute_command, external_tool_call_response, terminal_spawn/input/resize/kill, search_files, grep_in_files, list_in_directory, get_tree, read_file, write_file, edit_file, watch_file, unwatch_file, file_ops, list_memory, memory_history, memory_file_at_ref, memory_commit_diff, read/write/delete_memory_file, enable_memfs, list_models, list_connect_providers, connect_provider, disconnect_provider, chatgpt_usage_read, update_model, update_toolset, cron_* (8), skill_enable/disable, create_agent, agent_list/retrieve/create/update/delete, conversation_list/retrieve/create/update/recompile/fork/messages_list/compact, get_cwd_map, get/set_reflection_settings, get_experiments/set_experiment, channel_* (~20), remove_queue_item, search_branches, checkout_branch, secret_list, secret_apply.

### Outbound frames zc likely ignores (with value)
| Frame | What | Value for zc |
|---|---|---|
| `memory_updated {affected_paths[]}` | pushed on every memory write | live-refresh /memory views; transcript "✦ memory updated: system/persona.md" indicator — **high, cheap** |
| `skills_updated`, `crons_updated` | invalidation pushes | auto-refresh /skills & /crons overlays instead of stale-until-reopen |
| `file_changed {path,lastModified}` | fires for `watch_file`d paths | future file-viewer pane |
| `update_queue` kinds/sources | full snapshot w/ typed items | badge cron/subagent/channel-injected queue items distinctly |
| `retry` / `loop_error` / `status` deltas | see shortlist #6 | statusline/transcript notices |
| `terminal_output/spawned/exited` | PTY channel | see §4 |
| `pending_control_requests` in device_status | see shortlist #1 | correctness |
| `should_doctor` in device_status | harness says memory needs /doctor | one-line hint under input — trivial |
| `cwd_revision`, `cwd_map`, `boot_working_directory` | monotonic cwd signal + per-conversation cwd persistence (also `get_cwd_map`) | correct /cd display across conversations; detect rejected stale cwd |
| `mod_commands[].args` | palette arg hints for mod commands | check zc palette renders these |

### Notable command details
- **External/client tools** [WIRE, big]: `runtime_start.external_tools[]` registers controller-owned tool definitions (name/description/JSON-schema params, optional hidden `scope_id`); agent calls arrive as `external_tool_call_request` → client answers `external_tool_call_response` (MCP-style content array). Per-turn gating via `input.client_tool_allowlist` and `external_tool_scope_ids`. zc could expose zc-side tools (e.g. "ask user via picker", "notify"). Moderate effort, opens a whole capability class.
- **`create_agent`** [WIRE]: personality presets `memo|tutorial|blank|linus|kawaii` + model + tags + pin_global — first-class agent creation. zc's /system & /personality shell out to `node -e` preset extraction; *creation* flow (e.g. "new agent" in picker) can be pure wire. Also `runtime_start.create_agent` accepts full `AgentCreateParams` + `memfs:false` for throwaway worker agents.
- **`enable_memfs`** [WIRE]: zc's /memfs edits settings.json `agents[]` directly; an official wire path exists for *enabling* (still need settings edits for disable/aliases — but prefer wire where it exists, avoids the settings-cache race).
- **`search_branches` / `checkout_branch`** [WIRE]: git branch list (name/is_current/is_remote, filtered, cwd-scoped) + checkout with `create` flag → `/branch` picker for zc, pairs with git_context statusline.
- **File suite** [WIRE]: `read_file`, `write_file`, `edit_file` (old/new string, replace_all, `expected_replacements` validation, returns `start_line` + replacements count), `list_in_directory`, `get_tree`, `search_files` (mtime-ranked substring, powers @-completion), `grep_in_files` (IDE-grade: regex/case/whole-word/glob, line+column, context_lines before/after, max 500 matches). `grep_in_files` is unused by zc → could power a real find-in-files pane far beyond /search's transcript scan. `file_ops` is an Egwalker CRDT channel for Desktop's collaborative editor — skip (heavy), but know it exists.
- **`list_memory {include_references:true}`** [WIRE]: returns parsed `[[path]]` reference edges per memory file — the Memory Palace graph data. zc /memory could render a links/graph view basically for free.
- **`chatgpt_usage_read`** [WIRE]: OAuth ChatGPT plan rate-limit windows. zc filters OAuth providers out of /connect → low value, skip.
- **`conversation_create/fork.acting_user_id`**: cloud attribution relay — N/A for local backend.
- **`change_device_state`** payload also accepts `agent_id`/`conversation_id` (device default binding) — mostly desktop; note if zc ever surfaces "set default conversation".
- **DevicePermissionMode is exactly 3 modes**: `standard|acceptEdits|unrestricted` (legacy `default`/`bypassPermissions` migrated). Memory/UI claims of "4 modes" should be re-verified — there is no plan mode at this protocol version. (`permissions/mode.ts`, `protocol_v2.ts:155`)

## 2. Native TUI behaviors zc lacks

- **Bash mode** — shortlist #4. Also ships a `BashPreview`/syntax-highlighted command display and bash-command output caching per LET-7199 input locking.
- **Paste system** — shortlist #8: paste-registry with placeholder substitution on submit; Ctrl+V (and Ctrl+Shift+V path) image import; file-path paste detection (drag-drop of image paths converts to [Image #N]); HEIC read support exists tool-side.
- **Queue defer (Ctrl+D)** — shortlist #5.
- **Desktop notifications** [CLIENT] — `\x07` bell on awaiting-input (modern terminals convert to desktop notification when unfocused) + Notification hooks fire-and-forget. zc has nothing; trivial add on approval-needed/turn-complete-unfocused. (`app/notifications.ts`)
- **Token smoothing** — `hooks/use-token-smoothing.ts` paces token display for smooth streaming feel (vs bursty frame arrival). Consider if zc streaming feels choppy.
- **Spinner/status** — StreamingStatusSpinner + `spinners/animations.ts` frame-cycle machinery, ShimmerText, CompactingAnimation during /compact, BlinkDot/BlinkingSpinner. (crush audit covers zc's spinner story; native's is modest.)
- **Context chart** — `/context` renders full `context_window_overview` breakdown (system/core/summary/functions/messages tokens) + a per-turn token history sparkline with compaction-event marks (`helpers/context-chart.ts`, `context-tracker.ts`). zc /context is flat text by comparison. [LOCAL-ish: breakdown fetched via SDK `conversations.contextWindow`-style call — check wire availability before promising.]
- **ExitStats** — on quit: session duration, tokens, cost, agent name + pin hint, alien mascot. Cute closure; zc exits cold. [CLIENT]
- **TrajectorySummary** — "Worked for Xm · N steps" collapsed turn summaries. [CLIENT]
- **HelpDialog** — tabbed (Commands ⇄ Shortcuts), j/k navigable, includes custom commands with source/namespace annotations. zc /help is flat.
- **Conversation-switch alert** — notice when the conversation was switched elsewhere (another client). Minor but relevant since zc + native TUI may share an agent.
- **Terminal title** — zc has this (tea.View.WindowTitle). Native also has WindowTitlePicker for choosing title format — skip.
- **`/terminal`** — installs shift+enter keybindings into terminal emulator configs (iTerm2 etc.) [CLIENT]; irrelevant for zc (bubbletea v2 kitty protocol already gives shift+enter). Skip, but explains the command's existence.
- **Release notes on upgrade** — versioned markdown shown once per upgrade. zc has version-drift warning; could show "letta-code upgraded under you: re-verify protocol" nudge instead (Jack's protocol-instability rule).
- **Client-loop hooks gap** ⚠️ — `SessionStart` (stdout-on-exit-2 injected into first message as system-reminder), `Notification`, and `UserPromptSubmit` hooks execute in the *native client's* loop (`use-submit-handler.ts:3866`, `notifications.ts`), not in the listener turn path (grep of listener turn files shows no references). Remote/zc sessions likely never fire them, while PreToolUse/PostToolUse still run in the listener where tools execute. zc could run these hook events itself from the merged settings it already reads for /hooks. **Verify with Ecosystem Expert before building.**

## 3. Command registry diff (66 native commands)

**Already shipped in zc** (no action): /agents /model /init /doctor /remember /reflect /skills /skill-creator /memory /sleeptime /experiments /search /system /personality /pin /memfs /reasoning-tab /description /recompile /context /bg /compaction /memory-repository /statusline /feedback /fork /title /rename /cd /usage /export /subagents /connect /secret /mods /hooks /profiles /clear /compact /reload /context-limit /toolset /new /help + zc-only /crons /tidy /jobs /audit /mode /quit.

**Consciously skipped** (per no-alias rule / scope-outs): /resume /chdir /exit /set-max-context /reflection /login /logout /link /unlink (deprecated) /ade (cloud ADE browser-open).

**MISSED / newly classified:**
| Cmd | What | Wire? | Verdict |
|---|---|---|---|
| `/btw <q>` | fork + side question in ephemeral pane | **fully wire** (fork hidden + scoped input) | HIGH — un-defer it; shortlist #3 |
| custom `.commands/*.md` | user/project prompt commands | client-side | HIGH — shortlist #7 |
| `/stream` | toggle token streaming on/off | client rendering choice | LOW-MED — trivial toggle (render only on stop_reason vs live) |
| `/download` | export AgentFile `.af` | in-process SDK call [LOCAL] | LOW — shell-out to headless if ever asked |
| `/palace` | open Memory Palace web UI in browser | opens URL [CLIENT] | LOW — `xdg-open`; pairs with list_memory references |
| `/pinned` | pinned-agents browser | settings.json | SKIP — zc pickers already ★-sort |
| `/unpin` | unpin current agent | settings.json | verify zc /pin toggles both ways; else 5-line add |
| `/install-github-app` | GitHub Action setup flow | in-process gh flow | SKIP (niche; native handles) |
| `/reflect-arena` | blind A/B reflection model comparison | in-process experiment | SKIP for now — note it exists for Jack's reflection interests |
| `/permissions` | referenced in 0.25.7 release notes; **no longer in registry** (mode cycling + settings replaced it) | — | no action; don't add |

**Headless subcommands staying native** (shell-out inventory for zc): `letta agents/memory/memfs/messages/skills/mods/cron/dream/channels/connect/environments/remote/setup/backend/local-backend/update/version`. `letta messages` notably exists for message ops — an alternative backend for /search if the jsonl-scan ever breaks.

## 4. Session/runtime features

- **Terminal PTY over wire** — `terminal_spawn(cols,rows,cwd)` → `terminal_spawned{pid}` → bidirectional `terminal_input`/`terminal_output` + `terminal_resize`/`terminal_kill`/`terminal_exited`. Full remote terminal, conversation-cwd aware. zc's "terminal PTY" idea is confirmed wire-ready; effort is mostly the bubbletea terminal emulator widget. MED-HIGH effort, HIGH wow.
- **Approval payload details** — `can_use_tool` carries `blocked_path` (which permission path tripped) and `DiffPreview` modes `advanced|fallback|unpreviewable` with structured hunks. zc renders diffs; check it also surfaces `blocked_path` and the fallback/unpreviewable reasons rather than blank previews.
- **Queue semantics** — queue lives server-side (`update_queue` snapshots, kinds/sources); TUI-side defer mode is client-held. `remove_queue_item` responses complete the edit loop. Interrupt-queue tests confirm abort preserves queued items.
- **Compaction** — auto-compaction lives in `backend/local/compaction.ts` (server-side); client sees compaction via status frames + context stats. zc already sets `compaction_settings` via agent_update. Remaining UX: CompactingAnimation-equivalent + context-% warning color ramps.
- **Settings precedence** — projectLocal > project > global merged for hooks (known); `remote-settings.json` per-conversation permission modes for unattended turns (known, in memory). No new gaps found.
- **Recovery machinery** — listener has recovery lease + recoverable-notices + reconnect reminders; client contract is just `sync`/`runtime_start` + pending_control_requests + stale-approval probing flags (`recover_approvals`). Shortlist #1/#10 cover zc's part.
- **Experiments** — `ExperimentId`s at this version: `conversation_titles`, `desktop_conversation_bootstrap`, `tui_cron`. zc /experiments already lists via wire; nothing new.

## 5. 0.28.x-era items a frontend should surface

(Shallow clone — no changelog/git history; inferred from source present at 0.28.8.)
- `executing_tool_call_ids` in LoopState (self-healing executing set) — newer addition per comment; adopt it.
- `cwd_revision` monotonic counter — designed for clients to detect stale-cwd rejections.
- `mod_commands` advertised separately from `supported_commands` (zc uses this — confirm args hints).
- `conversation_titles` experiment — when enabled, titles auto-generate; zc statusline shows titles, so /experiments enabling this is a nice default suggestion.
- Provider fallback (`listener/provider-fallback.ts`) — listener silently falls back models on provider failure; surfaced via status deltas. Render `status` frames (shortlist #6) and you get this for free.

---

## Suggested batching for Jack

- **Batch A (correctness, ~half day)**: pending_control_requests replay · sync-on-reconnect · abort_message request_id ack · blocked_path/fallback in approval modal · 3-vs-4 permission-mode audit.
- **Batch B (daily-driver feel)**: git_context statusline · loop-status/retry/loop_error/status rendering · should_doctor hint · memory_updated/skills_updated/crons_updated refresh · queue kinds + remove_queue_item + Ctrl+D defer · bell notifications.
- **Batch C (features)**: bash mode · /btw · custom commands · /stream toggle · TodoWrite/Plan/trajectory rendering · context chart.
- **Batch D (bigger bets)**: image paste · grep_in_files find pane · external client tools · terminal PTY · /branch picker.
