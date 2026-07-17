# Protocol transition audit — "would the native Ink UI have an analog?"

Method (Jack's framing, 2026-07-17): for every piece of state the native Ink TUI
renders from or event it fires, ask — *if we converted the native app into a
protocol client, does the item already have a protocol analog, or must we add
one?* Ground truth = reading `src/cli/app/*` in the fork, NOT the ecosystem
expert.

Status legend: **EXISTS** (protocol carries it 1:1) · **PARTIAL** (carried but
flattened / lossy) · **MISSING** (no analog) · **CLIENT-LOCAL** (correctly never
crosses the wire per SPEC principle 3).

Native turn-state ground truth (important): the native in-process TUI does NOT
consume the 8-state `LoopStatus`/`update_loop_status` — that is a **listener
projection for remote observers** (`protocol_v2.ts:479` `LoopStatus`,
`:538 update_loop_status`). In-process it renders from `ExecutionPhase`
(`phase-visuals.ts:3` = `requesting|thinking|toolUse|responding|null`),
`NetworkPhase` (`use-conversation-loop.ts:139` = `error|upload|download|null`),
`streaming: boolean`, and `executingToolCallIdsRef` (`AppCoordinator.tsx:592`).
Our ui-server is ALSO in-process (spawns the harness like headless), so the
right target is the native ExecutionPhase/NetworkPhase model — NOT the 8-state
listener enum.

---

## 1. Turn / loop status  ← Jack flagged this

| Native item (file:line) | Protocol analog | Status |
|---|---|---|
| `executionPhase` requesting/thinking/toolUse/responding (`phase-visuals.ts:3`) | `turn.status` sending/thinking/streaming/executing_tool | **PARTIAL** — we collapse thinking≠responding into "thinking/streaming"; native uses distinct spinner *visuals* per phase (`getPhaseVisual`). |
| `networkPhase` error/upload/download (`use-conversation-loop.ts:139`) | — | **MISSING** — no upload/download/network-error indicator. |
| `thinkingMessage` rotating themed text (`AppCoordinator.tsx:1045`, `thinking-messages.ts`) | — | **MISSING** — we render a static spinner label; native rotates flavored "thinking" lines. |
| `executingToolCallIdsRef` (`AppCoordinator.tsx:592`) | — | **MISSING** — can't authoritatively highlight *which* tool card is live. |
| retry surfacing (`isRetriableError`, "retrying attempt X" transcript lines, empty-response retry `use-conversation-loop.ts:2485`) | transcript lines only | **PARTIAL** — retries only appear if the stream emits them as chunks; no explicit "retrying" turn state. |
| `interruptRequested` (`:521`) | `turn.status: cancelling` + client-local esc | **EXISTS**. |
| stop reasons end_turn/cancelled/error | `turn.stop_reason` | **EXISTS**. |

**Recommendation:** enrich the `turn` frame to the native in-process model:
`{ phase: requesting|thinking|toolUse|responding|null, network: error|upload|download|null, thinking_message?: string, executing_tool_call_ids: string[], stop_reason? }`. Keep it derived server-side from the same stream hooks native uses (`makeExecutionPhaseHook`, `setNetworkPhase`) so it's native-faithful, not a re-derivation. This is the single highest-fidelity gap.

## 2. Token / context counters

| Native (file:line) | Analog | Status |
|---|---|---|
| `tokenCount` session tokens (`AppCoordinator.tsx:1030`) | `device.usage.total_tokens` | **EXISTS** (fed by `sessionStats.updateUsageFromBuffers`). |
| `usedContextTokens` (`:1035`) | context-tracker `lastContextTokens` (used by `/context`) | **PARTIAL** — drives `/context` live but NOT pushed in `device` for a persistent context% readout. |
| `trajectoryTokenBase`/`trajectoryElapsedBaseMs` (`:1038-1039`) | — | **MISSING** — native elapsed-time + trajectory token deltas (statusline/exit stats). |
| `billingTier` (`:983`) | `/usage` balance only | **PARTIAL**. |

**Recommendation:** add `context_tokens` + `context_window` to `device.usage` so the client can show a persistent `NNk/NNNk (NN%)` without running `/context`. Elapsed/trajectory = nice-to-have.

## 3. Queue

| Native (file:line) | Analog | Status |
|---|---|---|
| `queueDisplay` (`AppCoordinator.tsx:1405`), `queueMode` (`:1687`), `restoreQueueOnCancel` (`:1617`), dequeue effects | — | **MISSING** — entire queue surface absent. Server-side queueing works (runtime), but nothing renders queued items or the defer mode; `queue_defer_set` is a no-op toast. |

**Recommendation:** add a `queue` frame (native `QueueMessage[]` shape, `protocol_v2.ts:505`) pushed on enqueue/dequeue; client renders queued rows + a defer indicator. Mirrors `update_queue`.

## 4. Subagent / nested-tool activity

| Native | Analog | Status |
|---|---|---|
| subagent stream rollups / nested tool tree (native drops subagent stream_deltas, shows rollups) | transcript `Line[]` (subagent tool calls appear as lines) | **PARTIAL** — we get the lines via the accumulator, but no dedicated "N subagents running" activity line or nested tree grouping. |

**Recommendation:** low priority; the accumulator already yields the rollup lines. Add a subagent-activity line only if it feels thin in use.

## 5. Overlays / pickers (the unified selection)

| Native overlay | Analog | Status |
|---|---|---|
| agent / conversation / model / skills pickers | `selection` (tag agent/conversation/model/skills) | **EXISTS** (collapsed 2026-07-17). |
| message search | `/search` → `selection` tag=search (jump-on-pick) | **EXISTS**. |
| memory viewer | `/memory` → `selection` tag=memory (view file) | **EXISTS** (degraded: file list, not the tree viewer). |
| AskUserQuestion | `pending_approvals[].questions` + `approval_response.answers` | **EXISTS**. |
| SleeptimeSelector (`AppView.tsx:49`) | `/sleeptime` text command (native `getReflectionSettings`/`persist`) | **PARTIAL** — correct persistence via native helper, but text UX vs overlay selector. |
| ProviderSelector (`/connect`) | `/connect` list/connect/disconnect (native `connectProvider`) | **PARTIAL** — text UX; OAuth providers correctly refused. |
| profile confirm / worktree diff selector / reflection-arena choice (`:529-538`) | — | **MISSING** — niche confirm overlays. |

## 6. Command coverage (native registry → zc analog)

Handled server-side via native helpers (EXISTS/verified): `/context` (`renderContextUsage`), `/usage` (`formatUsageStats`), `/context-limit` (`applySetMaxContext`), `/search`, `/memory`, `/reflect` (`launchReflectionSubagent`), `/export`, `/compaction`, `/system` (`buildSystemPrompt`), `/personality` (`applyPersonalityToMemory`), `/secret`, `/memory-repository`, `/hooks`, `/mods`, `/connect`, `/pin` `/unpin` `/memfs` `/reasoning-tab`, `/description` `/recompile` `/sleeptime` `/skills` `/skill-creator` `/feedback` `/help`, `/model` `/agents` `/conversations` `/resume` `/new` `/cd` `/title` `/rename` `/fork`.

**MISSING commands** (native has them, zc does not):
- `/toolset` — switch toolset (native present; no zc analog). **Add** — it's `settingsManager` + `updateAgent` toolset, straightforward.
- `/experiments` — get_experiments/set_experiment. **Add** (flat wire) or acknowledge stub.
- `/btw` — `conversation_fork {hidden}` + input on forked scope. **Add** (fully wire-implementable per prior audit).
- `/mcp`, `/subagents` — honest stubs (no local-backend support / bigger work).
- `/context` persistent bar, `/palace`, `/ade`, `/install-github-app`, `/terminal`, `/channels` — browser/PTY/niche; out of scope or stub.

## 7. Notifications / toasts / footer

| Native | Analog | Status |
|---|---|---|
| toasts (transient events) | `toast` / `notification` frames | **EXISTS**. |
| `footerUpdateText` version-drift (`:1359`) | client drift warning | **EXISTS**. |
| focus-aware OSC99/777/bell notifications (crush-steal) | — | **MISSING** — no terminal notification on turn-complete when unfocused. |

## 8. Misc persistent state

| Native (file:line) | Analog | Status |
|---|---|---|
| `conversationSummary` (`:402`) | — | **MISSING** — right-sidebar summary; zc chose bottom statusline, arguably out of scope. |
| `agentLastRunAt` (`:891`) | — | **MISSING** — minor. |
| `currentToolset`/preference (`:806-809`) | device.toolset (names only) | **PARTIAL** — shown but not switchable (see `/toolset`). |
| reasoning variant of model (`fable` vs `fable-max`) | device.model.handle | **PARTIAL** — no reasoning-tier surface in device; `/model` picker handles switching. |
| `btwState` (`:553`) | — | **MISSING** (`/btw`). |
| `expandedToolCallId` (`:490`), `restoredInput` (`:1705`), `searchQuery` input | client-local | **CLIENT-LOCAL** ✓ (correct — never crosses wire). |

---

## Prioritized gap list (what to actually add)

**Tier A — turn fidelity (Jack's flag):**
1. Enrich `turn` frame → native ExecutionPhase + NetworkPhase + thinking_message + executing_tool_call_ids, derived from the same stream hooks native uses.
2. Add `context_tokens`/`context_window` to `device.usage` for a persistent context% readout.

**Tier B — missing surfaces:**
3. `queue` frame (native `QueueMessage[]`) + client rendering + defer indicator.
4. `/toolset`, `/experiments`, `/btw` commands (all wire-implementable server-side).

**Tier C — polish:**
5. Sleeptime/connect overlays (currently text-UX but functionally correct).
6. Focus-aware terminal notifications on unfocused turn-complete.
7. Subagent activity line; conversationSummary; elapsed/trajectory stats.

**Confirmed CORRECT (no action):** 8-state LoopStatus is listener-only — we
should NOT mirror it in-process. `/sleeptime` uses the native reflection
persistence helper (correct logic; only the selector UX degrades to text).
Client-local input state correctly stays off the wire.

---

## Implementation status (2026-07-17)

- **Statusline/panel API (openPanel)** ✅ — server evaluates mod panels with
  `order`, client places them by native slots (>1 above input, 1 product-status,
  0 primary override, <0 below). muscle-memory (order 20) verified rendering.
  Fix: `mod_panels.owner` must be a string (object broke Go decode).
- **Tier A #1 turn frame** ✅ — `turn` now carries `phase`
  (requesting/thinking/toolUse/responding), `network` (upload/download/error),
  `thinking_message`, `executing_tool_call_ids`. Client renders phase spinner
  verb, network glyph, and highlights the live tool card.
- **Tier A #2 context readout** ✅ — `device.usage` carries `context_tokens` +
  `context_window`; statusline shows `NNk/NNNk (NN%)`.
- **Tier B #3 queue** ✅ — `queue` frame; mid-turn messages enqueue (visible),
  drain one-per-turn after; `queue_defer_set` toggles the defer indicator.
- **Tier B #4 commands** ✅ — `/toolset`, `/experiments`, `/btw` (hidden fork).
- **Tier C** — focus-aware turn-complete notification (OSC9+bell) ✅;
  `/sleeptime` now a selection overlay ✅; subagent activity in statusline ✅.

**Remaining (small / by-design):** `/connect` stays a text command (secure
API-key entry needs a masked form — bigger lift; text works); `conversationSummary`
(zc uses a bottom statusline by design, not a right sidebar); elapsed/trajectory
stats (minor). Everything else in the audit is implemented.
