# Ink UI ↔ Harness Touchpoints (letta-code 0.28.9, HEAD d253471e)

Exhaustive enumeration of every touchpoint between the native Ink TUI layer (`src/cli/app/*`, `src/cli/components/*`, `src/cli/hooks/*`, `src/cli/commands/*`, `src/cli/helpers/*`, `src/cli/display/*`, `src/cli/mods/*`) and the harness ("server": `@/agent/*`, `@/backend`, `@/mods/*`, `@/hooks`, `@/permissions`, `@/settings-manager`, `@/queue`, `@/reminders`, `@/tools`, `@/experiments`, `@/cron`, telemetry). This table is the grounding for the zc UI protocol: every row must be representable in the protocol (or explicitly classified as pure-client / server-internal) for a thin-client TUI to reach native parity.

**Method**: import-graph sweep of the UI layer + targeted source reads. Cross-referenced against `src/types/protocol_v2.ts` (the current listener wire protocol).

**Coverage legend** (column 5):
- ✅ `frame_name` — representable today with existing frame(s)
- ⚠️ partial — frame exists but misses listed data/behavior
- ❌ MISSING — no wire representation; needs a new frame or fork patch
- 🖥 CLIENT — pure client-side concern (no protocol needed beyond existing data)
- ⚙ SERVER — harness-internal; moves wholly server-side in the thin-client split (UI never needed to see it, it only lived in the UI process because the TUI runs in-process)

**Direction legend**: `→` UI→harness command · `←` harness→UI event/data · `⇄` shared mutable state.

---

## 1. Turn engine (send / stream / loop)

`use-conversation-loop.ts` (2935 ln), `use-submit-handler.ts` (4099 ln), `helpers/accumulator.ts` (1600+ ln)

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 1.1 | Send message / start turn | `use-conversation-loop.ts:784` `sendMessageStream(conversationId, input, {agentId, overrideModel, preparedToolContext, allowResponseStateReuse})` | → | MessageCreate[] + opts → async stream | ✅ `input` (create_message); ⚠️ per-turn `overrideModel` not expressible (`update_model` is conversation/agent-scoped, not turn-scoped) | every turn |
| 1.2 | Stream chunk consumption | `accumulator.ts:937-1318` cases: `reasoning_message`, `assistant_message`, `user_message`, `tool_call_message`, `approval_request_message`, `tool_return_message`, `usage_statistics` | ← | `LettaStreamingResponse` chunks → `Line` model (user/reasoning/assistant/tool_call/error/event kinds, phases streaming/ready/running/finished) | ✅ `stream_delta` (`MessageDelta = {type:"message"} & LettaStreamingResponse`) | transcript rendering |
| 1.3 | Client tool execution boundaries | loop marks tool running/finished around local execution | ← | toolCallId, name | ✅ `client_tool_start` / `client_tool_end`; ✅ self-healing `update_loop_status.executing_tool_call_ids` | tool card spinners |
| 1.4 | Prepared scoped tool context | `use-conversation-loop.ts:780` `prepareScopedToolExecutionContext()` → `PreparedScopeToolContext` | ⚙ | agent state + toolset prep pre-turn | ⚙ SERVER (listener does this in its turn path) | correct toolset per turn |
| 1.5 | Pre-stream error classification & retry | `use-conversation-loop.ts:797-900` `getPreStreamErrorAction` (409 busy w/ backoff, stale-approval recovery, transient LLM) | ⚙ | retries w/ budgets, resume-via-stream-endpoint | ⚙ SERVER (listener owns turn lifecycle); ⚠️ client needs visibility: `stream_delta.retry` (attempt/max/delay) + `loop_error.is_terminal` exist | resilience, retry countdown UI |
| 1.6 | Stale-approval auto-deny rebuild | `use-conversation-loop.ts:832-860` `rebuildInputWithFreshDenials` + `getResumeDataFromBackend` | ⚙ | fetch pending approvals, auto-deny stale | ⚙ SERVER + ✅ `sync {recover_approvals}` / `device_status.pending_control_requests` for client-side recovery | crash/interrupt recovery |
| 1.7 | Resume in-flight run after reconnect/busy | `use-conversation-loop.ts:880-900` resume via conversation stream endpoint + otid lookup | ⚙ | otid → resumed stream | ⚙ SERVER; client equivalent = ✅ `sync` | seamless resume |
| 1.8 | Loop status transitions | loop sets thinking/executing/waiting states consumed by spinner/statusline | ← | 8-state enum | ✅ `update_loop_status` (`LoopState`, 8 states incl. RETRYING_API_REQUEST, WAITING_ON_APPROVAL, WAITING_ON_INPUT) | spinner, statusline, title blink |
| 1.9 | Usage/cost accumulation | `accumulator.ts:1318` `usage_statistics` chunks → `SessionStats` (`@/agent/stats`) | ← | tokens, cost per turn | ✅ `stream_delta` usage_statistics; ⚠️ local backend emits sparse fields (completion only) | /usage, ExitStats |
| 1.10 | Stop hooks after turn | `use-conversation-loop.ts:1510` `runStopHooks(...)` — can block stop & inject continuation | ⇄ | hook result → continue turn or stop | ❌ MISSING from listener turn path (fork patch: port call site; #1282) | Stop hooks |
| 1.11 | Mod turn-start cancellation | `use-conversation-loop.ts:98` `getTurnStartCancel()` (`@/mods/turn-start-cancel`) | ⇄ | mod-requested turn cancel | ⚙ SERVER (mod engine lives server-side); no client involvement needed | mods cancelling turns |
| 1.12 | Interrupt-recovery alert injection | `use-conversation-loop.ts:36` `INTERRUPT_RECOVERY_ALERT` prompt-asset prepended after interrupted turn | ⚙ | system-alert text | ⚙ SERVER | post-esc continuity |
| 1.13 | Synthetic tool_return injection on deny/interrupt | `use-conversation-loop.ts:1789,1990,2168` fabricated `tool_return_message` chunks | ⚙ | denial/interrupt results | ⚙ SERVER (listener fabricates equivalently); ✅ visible as stream deltas | denied-tool transcript rows |
| 1.14 | Token smoothing | `hooks/use-token-smoothing.ts` | 🖥 | paced token display | 🖥 CLIENT | smooth streaming feel |
| 1.15 | Compaction events + stats | `accumulator.ts` event kind w/ `contextTokensBefore/After`, trigger, message counts; `CompactingAnimation` | ← | compaction lifecycle + stats | ✅ stream event messages + `slash_command_start/end` for /compact; ⚠️ verify auto-compaction (not user-invoked) emits the same event deltas through listener | compaction UI |
| 1.16 | PreCompact hooks | `accumulator.ts:1479`, `use-submit-handler.ts:2220` | ⇄ | hook veto/context | ✅ fires in listener too (`websocket/listener/commands.ts`) | PreCompact hooks |
| 1.17 | Turn telemetry | `telemetry.trackError(...)` throughout loop | ⚙ | error events | ⚙ SERVER | diagnostics |

## 2. Approvals & permissions

`use-approval-flow.ts` (1163 ln), `use-queued-approval-submit.ts`, `helpers/approval-classification.ts`, `app/approval-diffs.ts`, components `ApprovalDialog/ApprovalPreview/Inline*Approval/PendingApprovalStub/ApprovalSwitch`

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 2.1 | Tool approval analysis | `use-approval-flow.ts:210,755` `analyzeToolApproval(toolName, args)` → `ApprovalContext` (`@/permissions/analyzer`) | → | args → risk class, suggestions, blocked_path | ✅ `control_request {subtype: can_use_tool}` carries tool, args, suggestions, `blocked_path`, DiffPreview | approval dialog |
| 2.2 | Approval decision + suggestion persistence | dialog result → approve/deny + `selected_permission_suggestion_ids`, `updated_input` | → | decision payload | ✅ `input {approval_response}` | allow-and-remember, AskUserQuestion answers |
| 2.3 | Auto-allowed tool execution | `use-conversation-loop.ts:17` `executeAutoAllowedTools` | ⚙ | pre-approved tools run w/o dialog | ⚙ SERVER (permission engine server-side) | acceptEdits/unrestricted modes |
| 2.4 | Diff previews for file edits | `app/approval-diffs.ts`, `AdvancedDiffRenderer.tsx` | ← | `DiffPreview` modes advanced/fallback/unpreviewable, `DiffHunk[]` | ✅ in `can_use_tool` payload | edit approval diffs |
| 2.5 | Question-tool approvals (AskUserQuestion) | `app/approval-questions.ts`, `InlineQuestionApproval.tsx` | ⇄ | questions → answers via `updated_input` | ✅ same approval frames (remote clients must use `updated_input`; native bypasses in-process) | question forms |
| 2.6 | Permission mode read/set | `permissionMode` singleton (`@/permissions/mode`), InputRich border, shift+tab | ⇄ | standard \| acceptEdits \| unrestricted | ✅ `change_device_state {permission_mode}` + `device_status.current_permission_mode` | mode cycling |
| 2.7 | Permission denial formatting | `formatPermissionDenial` (`@/permissions/format-denial`) | ⚙ | denial text for transcript | ⚙ SERVER (comes back as tool_return delta) | denial rows |
| 2.8 | Pending approval on resume/switch | `getResumeDataFromBackend(agent, convId)` in loop, AppCoordinator, AppView, switching, queued-submit | → | pending approvals for conversation | ✅ `device_status.pending_control_requests` + `sync {recover_approvals}` | approval survives restart |
| 2.9 | Queued approval submit | `use-queued-approval-submit.ts` (answer approval while turn queued elsewhere) | → | ordered approval + queue interplay | ✅ approval_response + `update_queue`; semantics live server-side | approval during busy |
| 2.10 | PermissionRequest hooks | via `permissions/checker.ts` | ⚙ | hook veto | ✅ fires everywhere (shared core) | permission hooks |

## 3. Interrupts

`use-interrupt-handler.ts` (420 ln)

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 3.1 | Interrupt active turn | esc → abort stream, `INTERRUPTED_BY_USER` | → | abort signal | ✅ `abort_message` (+ `request_id` → `abort_message_response {aborted}`) | esc interrupt |
| 3.2 | Interrupt subagents | `use-interrupt-handler.ts:10` `interruptActiveSubagents()`, `getSubagents()` | → | cascade to subagents | ⚙ SERVER (listener abort path handles); ⚠️ verify subagent cascade parity in listener abort | esc kills Task tools |
| 3.3 | Interrupt during approval | deny-equivalent + queue preservation | → | — | ✅ abort + queue preserved (listener tests confirm) | esc on approval dialog |

## 4. Queue & task notifications

`utils/message-queue-bridge`, `queue/queue-runtime`, `QueuedMessages.tsx`, `utils/task-notifications`, tui-queue-adapter

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 4.1 | Queue runtime state | `QueueRuntime` (`@/queue/queue-runtime`) — items with kind/source | ⇄ | message/task_notification/cron_prompt/… × user/cron/subagent/channel | ✅ `update_queue` snapshots (typed kinds+sources) | queued messages UI |
| 4.2 | Harness→queue injection | `addToMessageQueue` (`message-queue-bridge`) — used by reflection-launcher, mods conversation handle, task notifications | ← | QueuedMessage | ✅ appears in `update_queue`; injection itself ⚙ SERVER | reflection results, mod sendMessage, subagent notifications |
| 4.3 | Queue item removal (load back to input) | desktop/native edit queued item | → | queue item id | ✅ `remove_queue_item` | queue editing |
| 4.4 | Queue defer (Ctrl+D) | InputRich defer toggle — hold queued msgs, `○` vs `>` bullets | 🖥 | client-held flush gate | 🖥 CLIENT (flush = send `input` later); ⚠️ if defer should survive client restart → needs server flag (MISSING, minor) | Ctrl+D defer |
| 4.5 | Task notifications extraction | `extractTaskNotificationsForDisplay` (`@/utils/task-notifications`) parses `<task-notification>` from queued msgs | ← | structured notification (task id, status, summary) | ⚠️ partial: raw text arrives in queue/messages; parsing is 🖥 CLIENT (or new structured frame) | background task toasts |
| 4.6 | Queue coalescing rules | tui-queue-adapter / coalescing-parity tests | ⚙ | dedupe/merge policy | ⚙ SERVER (listener has parity implementation) | clean queue |

## 5. Subagents

`@/agent/subagent-state` (14 imports), `SubagentGroupDisplay/Static`, subagent-display tests

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 5.1 | Subagent state store subscription | `getSnapshot/subscribe` from `@/agent/subagent-state` | ← | `SubagentSnapshot[]` (id, type, status, own conversation_id) | ✅ `update_subagent_state` (emitted on every mutation) | subagent activity lines |
| 5.2 | Subagent stream deltas (nested tools) | `stream_delta.subagent_id` routing | ← | child tool calls/results | ✅ `stream_delta {subagent_id}` (zc currently drops these — render as nested tree) | live subagent tool tree |
| 5.3 | Subagent lifecycle context for mods | `cli-mod-context.ts:1` `getSubagentLifecycleContext` | ⚙ | mod event context | ⚙ SERVER | mod subagent events |
| 5.4 | SubagentStop hooks | `tools/impl/task.ts` (shared) | ⚙ | — | ✅ fires everywhere | subagent hooks |
| 5.5 | Subagent definitions manager | `SubagentManager.tsx` ← `@/agent/subagents` (FS: .agents/subagents defs) | → | list/create/edit subagent presets | ❌ MISSING (no wire cmd; zc shipped /subagents via FS — must move server-side or new frames) | /subagents |

## 6. Mod engine surface

`cli/mods/*` (capabilities, local-mod-loader, use-local-mod-adapter, command-runtime, local-backend-mod-events), `ModPanelRow.tsx`, `display/product-status/*`, `helpers/cli-mod-context.ts`

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 6.1 | Mod adapter creation + capability declaration | `use-local-mod-adapter.ts:31` `createModAdapter({getBackend, getClient, agentModsDirectory})` w/ `TUI_MOD_CAPABILITIES` (everything true) | ⚙ | adapter + registry snapshot | ⚙ SERVER in thin-client split; **fork patch: listener adapter declares panels/lifecycle/compact/llm true** | mods load |
| 6.2 | Registry snapshot subscription | `useSyncExternalStore(adapter.subscribe, getSnapshot)` — registry.ui.panels, tools, commands; `hadModPanels`, `hasModSources`, `isLoading` | ← | reactive registry | ❌ MISSING — needs `mod_registry`/`mod_panels` push frames (only `device_status.mod_commands` exists today) | panel rows, /mods listing |
| 6.3 | Panel rendering | `ModPanelRow.tsx:48` `renderModPanelLines(panel, rowWidth, context)` — render is a pure fn → lines; order semantics (<0 below input, 0 primary, 1 product-status, >1 above) | ← | `ModPanel {id, order, render(ctx)→lines, updatedAt}` | ❌ MISSING — **the** statusline gap; fork patch: server-evaluated renders shipped as `mod_panels {placement, lines}` on change + width param | muscle-memory panel, statusline mods |
| 6.4 | Default product-status panel | `display/product-status/default.ts` `createDefaultProductStatusPanel` — spinner + background agents ("dreaming") | ← | synthesized ModPanel (order 1) from `background_processes` | ⚠️ partial: `device_status.background_processes` has the data; panel synthesis is 🖥 CLIENT | dreaming spinner row |
| 6.5 | Statusline renderers (legacy) | `display/statusline/*` (types, formatting, default-renderer-activation) + deprecation trap `mod-engine.ts:1094` | ← | deprecated → openPanel | superseded by 6.3 | old /statusline mods |
| 6.6 | Mod commands | `device_status.mod_commands` + `command-runtime.ts` execution w/ deprecated-API trap | → | `{name, description, args}` → execute | ✅ `mod_commands` + `execute_command`; ⚠️ `args` hints — verify client surfaces | /audit, /jobs in palette |
| 6.7 | Mod events emission (conversation_open/close, compact, llm, turns, tools) | `AppCoordinator.tsx:1178` `modAdapter.events.emit("conversation_close",…)`; `local-backend-mod-events.ts` wires LocalBackend → mod events | ⚙ | ModContext payloads | ⚙ SERVER; **fork patch: enable lifecycle/compact/llm event capabilities in listener adapter** (currently false) | mod lifecycle correctness |
| 6.8 | Mod reload | `/reload` → `adapter.reload()`; `/mods reload` remote | → | reload + diagnostics | ✅ `execute_command reload` (in SUPPORTED_REMOTE_COMMANDS) | /reload |
| 6.9 | Mod diagnostics | `mods/diagnostics/latest.json` + /mods UI | ← | load errors per phase | ❌ MISSING wire; 🖥 zc reads file today (same box); could ride a `mod_registry` frame | /mods diagnostics |
| 6.10 | Mod-provided providers | `AppCoordinator.tsx:49` `subscribePiProviderRegistry` (dev pi-provider registry) | ⚙ | provider registration | ⚙ SERVER; listener caps note per-agent scoping forced false | local model providers |
| 6.11 | Mod learning harness | `@/mods/learning-harness` (3 imports; /mods starts background learning runs) | ⚙ | background runs | ⚙ SERVER; surfaced via `background_processes` | mod learning |
| 6.12 | Mod conversation handle (sendMessageStream into conversations) | `use-submit-handler.ts:132` `createModConversationHandle` | ⚙ | mod-initiated real user messages | ⚙ SERVER; visible as normal stream/queue | jobs-mod result delivery |

## 7. Hooks (11 events)

`@/hooks` runners; settings-file schema in `hooks/types.ts` + `hooks/writer.ts`

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 7.1 | UserPromptSubmit | `use-submit-handler.ts:757` — before send; can block/mutate prompt | ⇄ | prompt → allow/deny/mutate | ❌ MISSING in listener input path (fork patch; #1282) | prompt hooks |
| 7.2 | SessionStart | `use-submit-handler.ts:1919,2011,2120`, `use-conversation-switching.ts:424`, `AppCoordinator` (first message; stdout exit-2 injected as system-reminder; feedback captured) | ⇄ | session ctx → injected reminder | ❌ MISSING in listener (fork patch; #1282) | session-start context injection |
| 7.3 | Stop | `use-conversation-loop.ts:1510` — may force continuation | ⇄ | turn result → continue/stop | ❌ MISSING in listener (fork patch; #1282) | stop hooks |
| 7.4 | SessionEnd | `AppCoordinator.tsx:1164` `runSessionEndHooks(durationMs,…)` on quit/switch (**wired at 0.28.9 — supersedes earlier "dead code" finding**) | → | duration, agent, conversation | ❌ MISSING in listener; client can't trigger equivalent | session-end hooks |
| 7.5 | Notification | `app/notifications.ts:14` `runNotificationHooks(message, level)` alongside bell | → | message, level | ❌ MISSING in listener; needs `notification` frame or server-side trigger points | notification hooks |
| 7.6 | PreToolUse/PostToolUse/PostToolUseFailure | `tools/manager.ts` (shared core) | ⚙ | — | ✅ fires everywhere | tool hooks |
| 7.7 | PermissionRequest | `permissions/checker.ts` | ⚙ | — | ✅ fires everywhere | permission hooks |
| 7.8 | SubagentStop | `tools/impl/task.ts` | ⚙ | — | ✅ fires everywhere | subagent hooks |
| 7.9 | PreCompact | accumulator + listener commands.ts | ⚙ | — | ✅ fires in listener | compact hooks |
| 7.10 | Hooks config management UI | `HooksManager.tsx` ← `@/hooks/types` schema + `@/hooks/writer` (writes 4 settings files, merged projectLocal>project>global) | → | CRUD on hook defs | ❌ MISSING wire (zc reads files; writer logic should move server-side or ride read/write_file) | /hooks manager |

## 8. Commands (all 64 registry entries + routing)

Registry: `commands/registry.ts` (stub handlers; real impls in UI layer). Routing: `command-routing.ts` — 23 INTERACTIVE (bypass queue, open overlays), 17 NON_STATE (run while busy), rest queue behind turn; custom commands never bypass; mod commands per `runWhenBusy`. **The queue-bypass classification itself is protocol-relevant metadata** (❌ MISSING: `supported_commands` is a flat string list — no interactive/non-state/busy semantics carried).

Implementation classification per command (impl location → what a thin client needs):

| Command | Implementation | Harness deps | Wire coverage for thin client |
|---|---|---|---|
| /agents | AppView overlay | `agent_list`-equiv via backend, favorites | ✅ `agent_list`; pin ❌ (settings.json) |
| /model | ModelSelector overlay | `available-models`, byok-providers, settingsManager | ✅ `list_models` + `update_model`; ⚠️ byok key-entry flows partial (`connect_provider` API-key only) |
| /init | sends init prompt + `init-command.ts` (git snapshot, subagent state) | gatherGitContextSnapshot, subagent snapshot | ✅ `execute_command init` (in 11) |
| /doctor | sends doctor prompt | — | ✅ `execute_command doctor` |
| /remember | sends remember prompt | — | ✅ `execute_command remember` |
| /reflect | `reflection-launcher.ts`: memory worktrees, subagent spawn, queue injection | memory-worktree, subagent-state, backend, telemetry | ❌ MISSING wire (zc shells out to `letta dream`); ⚙ SERVER candidate |
| /reflection | reflection settings overlay | get/set reflection settings | ✅ `get/set_reflection_settings` (runtime-scoped) |
| /reflect-arena | `reflection-arena.ts` + HF upload | backend, telemetry | ❌ MISSING; scope-out candidate |
| /skills | SkillsDialog | `@/agent/skills` FS scan + enable/disable | ⚠️ `skill_enable/disable` ✅; listing ❌ (`current_available_skills` hardcoded `[]`) |
| /skill-creator | sends prompt + skill frontmatter repair helper | — | ✅ prompt via `input` |
| /memory | MemoryTabViewer/MemfsTreeViewer | backend memory APIs | ✅ `list_memory` (+`include_references`), `read/write/delete_memory_file`, `memory_history`, `memory_file_at_ref`, `memory_commit_diff` |
| /palace | opens web UI | app-urls | 🖥 CLIENT (xdg-open) |
| /sleeptime | SleeptimeSelector | reflection settings | ✅ `get/set_reflection_settings` |
| /compaction | CompactionSelector | agent compaction_settings | ✅ `agent_update` |
| /context-limit | remote-supported | — | ✅ `execute_command context-limit` |
| /memfs | settings agents[] edit + `enable_memfs` | settingsManager, memory-runtime | ⚠️ `enable_memfs` ✅; disable/aliases ❌ (settings writes) |
| /search | MessageSearch ← `@/backend/message-search` + cache warm | conversation jsonl scan | ❌ MISSING wire cmd (zc scans files; should move server-side) |
| /connect | connect.ts + oauth flows + ProviderSelector | providers, auth/oauth, secrets-store | ⚠️ `list_connect_providers`/`connect_provider`/`disconnect_provider` ✅ API-key; OAuth ❌ (throws; native-only browser flow) |
| /clear | remote-supported | — | ✅ `execute_command clear` |
| /chdir, /cd | `helpers/chdir-command.ts` + `switchCurrentRuntimeWorkingDirectory` (imports listener code directly!) | listener cwd-change | ✅ `change_device_state {cwd}` + `cwd_map`/`cwd_revision`/`boot_working_directory` |
| /new | conversation create + switch | backend | ✅ `conversation_create` + `runtime_start` |
| /fork | `use-submit-handler.ts:1980` `backend.forkConversation` | backend | ✅ `conversation_fork` |
| /btw | `use-submit-handler.ts:2049` fork {hidden} + bg stream + BtwPane | backend fork + sendMessageStream | ✅ `conversation_fork {hidden:true}` + `input` on forked scope; pane 🖥 CLIENT |
| /pin, /unpin, /pinned | `@/agent/favorites` (settings.json) | settingsManager | ❌ MISSING wire (settings write + reload workaround) |
| /rename | agent_update / conversation title | backend | ✅ `agent_update`, `conversation_update` |
| /description | agent_update + `regenerateConversationDescription` | backend, SDK | ✅ `agent_update`; conversation desc regen ⚠️ (SDK summarize — server-side candidate) |
| /export | transcript export | backend messages | ✅ `conversation_messages_list`; formatting 🖥 CLIENT |
| /download (.af) | SDK agent file export | client SDK | ❌ MISSING wire; shell-out |
| /toolset | remote-supported + overlay | tools/toolset | ✅ `update_toolset` + `execute_command toolset` |
| /experiments | ExperimentSelector | experiments/manager | ✅ `get_experiments`/`set_experiment` |
| /reload | remote-supported | mod adapter | ✅ `execute_command reload` |
| /mods | mods.ts (list/install/learning) + packages.json + diagnostics | mods/package-installer, learning-harness | ⚠️ listing/diag ❌ (FS); reload ✅; install ❌ |
| /ade | open browser | app-urls | 🖥 CLIENT; cloud scope-out |
| /system | SystemPromptSelector + preset apply | agent presets, personality | ⚠️ presets exported (`agent-presets` pkg export); apply = memory writes ✅ + settings ❌ |
| /personality | PersonalitySelector + `applyPersonalityToMemory` | personality-presets | ✅ memory writes; preset data via pkg export |
| /subagents | SubagentManager | `@/agent/subagents` FS | ❌ MISSING (see 5.5) |
| /mcp | McpSelector/McpConnectFlow + `mcp-oauth` helper | SDK client.mcpServers (API backend only) | ❌ no local-backend support (known); honest stub |
| /secret | secret.ts (UI-routed) | utils/secrets-store | ✅ `secret_list`/`secret_apply` (plaintext values!) |
| /memory-repository | memory-repository.ts (git config in memfs) | memory-git | ❌ MISSING wire (git ops; zc does local git — server-side candidate) |
| /usage | SessionStats display | stats | ⚠️ from usage deltas (sparse locally) |
| /context | context chart | context-tracker + SDK contextWindow | ❌ MISSING: no wire cmd for `context_window_overview` (system/core/summary/functions/messages token breakdown) |
| /recompile | remote? | backend | ✅ `conversation_recompile` |
| /feedback | FeedbackDialog → api.letta.com POST | backend/api/metadata | 🖥 CLIENT (direct HTTP) |
| /help | HelpDialog | commands registry + custom + mod commands | 🖥 CLIENT from `supported_commands`+`mod_commands`+custom; ⚠️ needs desc/args metadata (`supported_commands` is bare ids — ❌ no descriptions on wire) |
| /hooks | HooksManager | hooks/types+writer | ❌ (see 7.10) |
| /statusline | statusline mod scaffold flow | mods | ❌ panels gap (6.3) |
| /title | conversation_update + summarize | backend | ✅ `conversation_update`; auto-title ✅ (conversation_titles experiment, server-side) |
| /reasoning-tab | settings toggle | settingsManager | ❌ (settings write) |
| /terminal | terminal-keybinding-installer (iTerm2 etc. config edits) | FS | 🖥 CLIENT; zc scope-out (kitty protocol native) |
| /install-github-app | install-github-app.ts gh flow | FS/gh | scope-out |
| /bg | background processes list | device state | ✅ `device_status.background_processes` |
| /exit | quit + ExitStats + SessionEnd hooks | stats, hooks | 🖥 CLIENT + 7.4 gap |
| /login, /logout | auth/oauth, logout-message | auth | scope-out (native CLI) |
| /stream | token streaming toggle | — | 🖥 CLIENT render choice |
| /compact | remote-supported | — | ✅ `execute_command compact` |
| /set-max-context | context limit variant | — | ✅ `execute_command context-limit` |
| /link, /unlink | deprecated | — | skip |
| /resume | conversation picker alias | — | ✅ `conversation_list` (zc no-alias rule) |
| /profiles | profile.ts (settings.json name→agent map) | settingsManager | ❌ (settings write + reload workaround) |
| Custom commands | `commands/custom.ts` — `.commands/*.md` + `~/.letta/commands/*.md`, frontmatter desc/argument-hint, namespacing, project-shadows-user | FS + frontmatter util | ❌ MISSING as wire concept: server-side FS scan + `custom_commands` advertisement (or client FS — but project `.commands` lives at server cwd) |

## 9. Input affordances

`InputRich.tsx` (1600+ ln), `PasteAwareTextInput`, `use-bash-handlers.ts`, `helpers/paste-registry.ts`, `helpers/clipboard.ts`, autocomplete components

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 9.1 | Bash mode (`!`) execution | `use-bash-handlers.ts:80` `spawnCommand` from `@/tools/impl/bash.js` + `shell-aliases.ts` expansion; output cached → `<bash-input>/<bash-output>` system-reminder prefix on next send | → | local shell exec in conversation cwd | ⚠️ options: 🖥 CLIENT exec (same box) using `device_status.current_working_directory`, or ✅ `terminal_spawn` PTY channel; reminder-prefix composition 🖥 CLIENT | ! bash mode |
| 9.2 | Paste registry (large text) | `paste-registry.ts` `allocatePaste`/`resolvePlaceholders` — `[Pasted text #N +X lines]` display vs actual | 🖥 | placeholder indirection | 🖥 CLIENT | large-paste placeholders |
| 9.3 | Image paste/import | `helpers/clipboard.ts` + `@/utils/image-resize` (sharp/imagemagick) → `[Image #N]` → image content part | → | image content parts in MessageCreate | ✅ `input` accepts image parts (listener `image-policy.ts` strict mode); resize ⚙/🖥 | Ctrl+V images |
| 9.4 | File @-autocomplete | `FileAutocomplete.tsx` + `helpers/file-autocomplete.ts` + `file-search-config`/`ignored-directories` | → | fragment → ranked paths (server cwd) | ✅ `search_files` (mtime-ranked, powers native @-completion) | @ completion |
| 9.5 | Slash autocomplete | `SlashCommandAutocomplete.tsx` (registry + custom + mods; settingsManager for recents) | ← | command metadata | ⚠️ ids ✅ (`supported_commands`/`mod_commands`); descriptions/args/order/hidden ❌ (client hardcodes today) | / completion |
| 9.6 | Input history | InputRich local history | 🖥 | — | 🖥 CLIENT | up/down recall |
| 9.7 | Shell context detection | `use-submit-handler.ts:148` `detectShellContext` | ⚙ | env snapshot in reminders | ⚙ SERVER | env context for agent |
| 9.8 | Queue defer toggle UI | InputRich Ctrl+D | 🖥 | see 4.4 | 🖥 CLIENT | defer |
| 9.9 | Terminal width/cursor hooks | `use-terminal-width`, `use-text-input-cursor`, `use-input-key-sequences` | 🖥 | — | 🖥 CLIENT | input feel |

## 10. Direct state reads (settings, model, context, git, stats)

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 10.1 | settingsManager (45 import sites) | reads/writes `~/.letta/settings.json` + project settings: pinned agents, profiles, recents, reasoning-tab, memfs flags, hooks, model prefs, onboarding flags | ⇄ | full settings object | ❌ MISSING generic settings frames; pieces covered (`enable_memfs`, experiments, reflection); **external-write cache race known** — thin client should NEVER write settings files directly; needs `settings_read`/`settings_patch` frames (fork) or per-feature frames | pins, profiles, prefs |
| 10.2 | Model info/handles | `getModelInfo`, `getModelInfoForLlmConfig`, `getAvailableModelHandles`, `prefetchAvailableModelHandles`, `model-handles`, `available-models` | ← | catalog + reasoning variants (id vs handle vs label) | ✅ `list_models` (catalog + available_handles + key-missing flags) | model picker, statusline model |
| 10.3 | Reasoning effort cycling | `use-reasoning-cycle.ts` (Tab) — variant ids per handle | → | model variant switch | ✅ `update_model` (variants are distinct ids) | Tab reasoning cycle |
| 10.4 | Context tracker + chart | `context-tracker.ts` (per-turn token history, compaction marks) + `context-chart.ts` (`ContextWindowOverview`) | ← | token breakdown + history | ❌ MISSING: context_window_overview has no frame; history derivable client-side from usage deltas (⚠️ sparse) | /context chart, % warning |
| 10.5 | Git context snapshot | `git-context.ts` `gatherGitContextSnapshot` (for init/session reminders), `getGitContext` (light) | ← | branch, dirty state, recents | ✅ `device_status.git_context {branch, recent_branches}`; full snapshot (dirty files for reminders) ⚙ SERVER | statusline branch, init context |
| 10.6 | Session stats | `SessionStats` class (`@/agent/stats`), snapshot for ExitStats | ← | tokens/cost/duration | ⚠️ derivable from usage deltas; no dedicated frame (`usage sparse locally`) | ExitStats, /usage |
| 10.7 | should_doctor / memory reminder | `helpers/memory-reminder.ts` + settingsManager | ← | flag | ✅ `device_status.should_doctor` | doctor hint |
| 10.8 | Reminders engine (system reminders pre-send) | `use-submit-handler.ts:137` `@/reminders/engine` + `reminders/state` + `session-context.ts` + `backfill.ts` + memory-git-sync reminder (`:3889`) | ⚙ | reminder text parts appended to input | ⚙ SERVER (listener input path must run the same engine — verify parity; fork checklist) | context injection |
| 10.9 | Experiments | `experimentManager` (4 sites) | ⇄ | flags | ✅ `get_experiments`/`set_experiment` | /experiments |
| 10.10 | Version / release notes | `getVersion` (9 sites), `release-notes.ts` → `releaseNotes` prop shown once per upgrade | ← | version string, md | ⚠️ `device_status.letta_code_version` (often null locally — known bug); release-notes ❌ (client FS) | version display, upgrade notes |
| 10.11 | Billing tier | `AppCoordinator.tsx:48` `getBillingTier` (API metadata) | ← | tier | ❌ cloud-only; scope-out local | cloud UI bits |
| 10.12 | Agent info bar | `helpers/agent-info.ts` + `AgentInfoBar.tsx` | ← | name, model, memfs status | ✅ `agent_retrieve` + `device_status` | header/statusline |
| 10.13 | Cron scheduler in TUI | `AppCoordinator.tsx:153` `@/cron` (tui_cron experiment — TUI process can hold scheduler lease) | ⚙ | lease + due-cron execution | ⚙ SERVER (app-server holds lease in zc topology); crons CRUD ✅ `cron_*` (8 cmds) + `crons_updated` | background crons |
| 10.14 | Memory filesystem root/status | `getScopedMemoryFilesystemRoot`, `isActiveMemfsEnabled` (11 imports) | ← | memfs path + enabled | ✅ `device_status.memory_directory` + `agent_retrieve`; `enable_memfs` | memfs indicators |
| 10.15 | Conversation-switch alert | `helpers/conversation-switch-alert.ts` (another client moved the conversation head) | ← | alert reminder | ⚠️ detectable via `conversation_retrieve`/messages list; no push frame (❌ minor) | switch alert |

## 11. Pickers / selectors / overlays (data sources)

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 11.1 | Agent browser | `AgentSelector.tsx` ← backend listAgents + favorites + model display | → | agents + pin state | ✅ `agent_list` (limit! default 20); pins ❌ (10.1) | /agents |
| 11.2 | Conversation picker | `ConversationSelector.tsx` ← backend list | → | conversations (title, last_message_at, archived/hidden) | ✅ `conversation_list` (+include_hidden) | /resume, startup |
| 11.3 | Model selector | `ModelSelector.tsx` (availability tests, byok, categories) | → | grouped catalog | ✅ `list_models`; byok add-key flow ⚠️ | /model |
| 11.4 | Skills dialog | `SkillsDialog.tsx` ← `@/agent/skills` | → | skills + sources + enabled | ⚠️ enable/disable ✅; listing ❌ (FS scan; `current_available_skills` = `[]` hardcoded) | /skills |
| 11.5 | Memory viewers | `MemoryTabViewer`, `MemfsTreeViewer`, `MemoryDiffRenderer` | → | files, history, diffs | ✅ memory frame suite (incl. `memory_updated` push) | /memory |
| 11.6 | Message search | `MessageSearch.tsx` ← `@/backend/message-search` (+ cache warm) | → | cross-conversation hits | ❌ MISSING wire (8. /search) | /search |
| 11.7 | Experiment/compaction/sleeptime/system/personality/reasoning selectors | respective components | → | current + options | ✅ experiments/agent_update/reflection_settings; presets via pkg export | overlays |
| 11.8 | Provider connect overlay | `ProviderSelector`, `McpConnectFlow`, `ConstellationLoginOverlay` | → | providers, auth methods | ⚠️ API-key ✅; OAuth ❌ | /connect |
| 11.9 | Help dialog | `HelpDialog.tsx` (tabbed commands/shortcuts, custom cmd annotations) | ← | command metadata | ⚠️ see 9.5 metadata gap | /help |
| 11.10 | Pin dialog | `PinDialog.tsx` ← favorites | → | pin CRUD | ❌ (10.1) | /pin |
| 11.11 | Feedback dialog | `FeedbackDialog.tsx` → API POST | → | text + metadata | 🖥 CLIENT | /feedback |
| 11.12 | Bg processes view | /bg ← `background_processes` | ← | bash/agent-task summaries | ✅ `device_status.background_processes` | /bg |

## 12. Conversation & agent management flows

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 12.1 | Conversation switching | `use-conversation-switching.ts` (875 ln): create/resume, approval recovery, SessionStart hooks, bootstrap reminders | → | switch + recovery bundle | ✅ `runtime_start {conversation_id}` + recovery frames; hooks gap 7.2 | Ctrl+P switching |
| 12.2 | Agent creation | `use-conversation-switching.ts:25` `createAgent` + `selectDefaultAgentModel`; personality presets | → | AgentCreateParams + personality | ✅ `create_agent` (presets memo/tutorial/blank/linus/kawaii) + `runtime_start.create_agent` (+`memfs:false` throwaway) | new agent flows |
| 12.3 | Agent state reconciliation | `AppCoordinator.tsx:36` `reconcileExistingAgentState` (preset upgrades, same-preset only) | ⚙ | prompt reconciliation | ⚙ SERVER | preset upgrades |
| 12.4 | Conversation bootstrap | `helpers/conversation-bootstrap.ts` (desktop_conversation_bootstrap experiment) | ⚙ | initial msgs | ⚙ SERVER | first-run UX |
| 12.5 | Session history recording | `recordSessionEnd` (`@/agent/session-history`) in submit handler + AppCoordinator | ⚙ | session rows | ⚙ SERVER | /resume ordering data |
| 12.6 | Conversation title auto-gen | `helpers/conversation-title.ts` `summarizeConversation` (title model, debounce) | ⚙ | title | ✅ server-side w/ `conversation_titles` experiment + `conversation_update` | auto titles |
| 12.7 | Model carryover on conversation switch | `AppCoordinator.tsx:24` `buildConversationModelCarryoverUpdate` | ⚙ | model persistence rules | ⚙ SERVER; ⚠️ `update_model {applied_to}` semantics cover client view | model stickiness |
| 12.8 | Memory worktrees (reflection isolation) | `@/agent/memory-worktree` (4 imports), `buildReflectionMemoryScope` | ⚙ | worktree lifecycle | ⚙ SERVER; listener has worktree-ownership/watcher modules | parallel reflection safety |
| 12.9 | Post-turn reflection gate | `helpers/post-turn-reflection.ts`, `reflection-gate.ts` | ⚙ | auto-reflection trigger | ⚙ SERVER; visible via `background_processes` + queue injection | sleeptime |
| 12.10 | Init background subagent | init-command + init-background-subagent test | ⚙ | init flow | ⚙ SERVER + prompt via input | /init |

## 13. Notifications, terminal chrome, exit

| # | Touchpoint | Source | Dir | Shape | protocol_v2 coverage | Native feature |
|---|---|---|---|---|---|---|
| 13.1 | Bell notification | `app/notifications.ts:12` `\x07` on awaiting-input | 🖥 | trigger points: approval needed, turn done unfocused | 🖥 CLIENT (trigger from `update_loop_status`/`control_request`); Notification hooks 7.5 ❌ | desktop notifications |
| 13.2 | Window title | `helpers/window-title-config.ts` (configurable items, spinner frames, action-required blink prefix) | 🖥 | title string from loop state | 🖥 CLIENT (data all wire-available) | terminal title |
| 13.3 | ExitStats | `app/ExitStats.tsx` (duration, tokens, cost, pin hint, mascot) | 🖥 | SessionStatsSnapshot | 🖥 CLIENT (⚠️ 10.6 sparse usage) | quit screen |
| 13.4 | Animated logo / shimmer / spinners | `AnimatedLogo`, `ShimmerText`, `BlinkDot`, `BlinkingSpinner`, `AnimationContext` | 🖥 | — | 🖥 CLIENT | polish |
| 13.5 | Clipboard write (copy) | helpers/clipboard | 🖥 | — | 🖥 CLIENT | copy support |

## 14. Telemetry & debug (server-internal)

| # | Touchpoint | Source | Dir | Coverage |
|---|---|---|---|---|
| 14.1 | telemetry.track* (9 import sites) | throughout UI | ⚙ | ⚙ SERVER (moves with the logic that emits it) |
| 14.2 | debugLog/debugWarn (19 sites) | throughout | ⚙/🖥 | each side logs its own |
| 14.3 | chunk-log helper | `helpers/chunk-log.ts` | 🖥 | 🖥 CLIENT debug (zc has ZC_DEBUG_FRAMES) |

---

## Summary counts

| Subsystem | Rows | ✅ covered | ⚠️ partial | ❌ MISSING | 🖥 client | ⚙ server-internal |
|---|---|---|---|---|---|---|
| 1. Turn engine | 17 | 6 | 2 | 1 | 1 | 7 |
| 2. Approvals | 10 | 7 | 0 | 0 | 0 | 3 |
| 3. Interrupts | 3 | 2 | 1 | 0 | 0 | 0 |
| 4. Queue | 6 | 3 | 1 | 0 | 1 | 1 |
| 5. Subagents | 5 | 3 | 0 | 1 | 0 | 1 |
| 6. Mods | 12 | 2 | 2 | 3 | 0 | 5 |
| 7. Hooks | 10 | 4 | 0 | 6 | 0 | 0 |
| 8. Commands | 64 cmds | ~31 | ~8 | ~14 | ~6 | ~5 |
| 9. Input | 9 | 2 | 2 | 0 | 5 | 0 |
| 10. State reads | 15 | 6 | 3 | 3 | 0 | 3 |
| 11. Pickers | 12 | 6 | 4 | 2 | 1 | 0 |
| 12. Conv/agent mgmt | 10 | 3 | 0 | 0 | 0 | 7 |
| 13. Chrome | 5 | 0 | 0 | 0 | 5 | 0 |
| 14. Telemetry | 3 | 0 | 0 | 0 | 1 | 2 |
| **Total** | **~181** (117 rows + 64 commands) | | | **~30 distinct missing/partial gaps** | | |

## Structurally significant MISSING items (fork patch / new-frame spec)

1. **Mod panels** (6.2, 6.3) — zero wire representation of the panel registry or renders; the single largest fidelity gap. Patch: listener adapter `panels:true` + server-evaluated `render(ctx)` → `mod_panels {panels:[{id, order, lines}]}` pushed on registry change/width change.
2. **Client-loop hooks** (7.1–7.5) — `UserPromptSubmit`, `SessionStart`, `Stop`, `SessionEnd`, `Notification` never fire in the listener turn path. **Correction to prior intel: SessionEnd IS wired in the TUI at 0.28.9 (AppCoordinator.tsx:1164)** — all five need listener call-site ports (#1282 covers three; SessionEnd/Notification additionally).
3. **Mod event capabilities** (6.7) — lifecycle/compact/llm events disabled for listener-loaded mods; one-line capability change + verify emit points exist in listener paths.
4. **Settings surface** (10.1) — no generic settings read/patch frames; pins/profiles/reasoning-tab/memfs-aliases all require settings.json writes (and the external-write cache race makes client-side writes actively dangerous). Needs `settings_read`/`settings_patch` or per-feature frames.
5. **Command metadata** (8, 9.5, 11.9) — `supported_commands` is bare ids: no descriptions, arg hints, ordering, hidden flags, or queue-bypass classes (INTERACTIVE/NON_STATE). Thin client must hardcode what the server already knows. Patch: enrich to structured entries.
6. **Custom commands** (8) — project `.commands/` lives at server cwd; no wire discovery/advertisement.
7. **/search** (8, 11.6) — `@/backend/message-search` has no wire command; cross-conversation search should be a server capability (`search_messages`).
8. **Context window overview** (10.4) — no frame for token breakdown powering /context chart and % warnings.
9. **Skills listing** (11.4) — `current_available_skills` hardcoded `[]`; needs real skill enumeration frame.
10. **Subagent definitions** (5.5) — /subagents manager is pure FS; no wire CRUD.
11. **Mod registry/diagnostics** (6.2, 6.9) — /mods listing and load-error surfacing have no frames.
12. **Reflection launch** (8 /reflect) — worktree + subagent orchestration is in-process only; needs a `start_reflection` command (or stays shell-out).
13. **Hooks config CRUD** (7.10) — writer/merge logic in-process; thin client needs frames or (interim) read/write_file.
14. **Per-turn model override** (1.1) — `overrideModel` not expressible; `update_model` changes persistent scope.
15. **Memory-repository ops** (8) — git remote config/push for memfs has no frames.

## Notes for the protocol spec

- Everything marked ⚙ SERVER is an argument **for** the fork architecture: those subsystems already exist in the harness and simply run server-side; the thin client never reimplements them. The listener already hosts many (reminders parity, queue coalescing, approval recovery, title generation) — parity of the remainder (Stop/SessionStart/UserPromptSubmit hooks, mod capabilities) is exactly the fork patch set.
- Everything marked 🖥 CLIENT stays in Go and is unaffected by transport (input feel, paste registry, bell, title, ExitStats rendering, token smoothing).
- The ✅ set (~60% of touchpoints) validates "protocol_v2 as base vocabulary, superset it": the existing wire already carries the majority of native data flows.
