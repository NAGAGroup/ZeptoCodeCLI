# zc UI Protocol Gap Analysis — Native TUI Behaviors vs Our Implementation

> Generated from deep source reading of letta-code 0.28.9 (AppCoordinator, AppView, InputRich, PasteAwareTextInput, ApprovalSwitch, ToolCallMessageRich, and related components) cross-referenced against zc's ui-server.ts and Go client.
>
> Focus: *HOW* the native TUI achieves its UX quality, not just *WHAT* features exist.

## 1. INPUT / EDITING / SELECTION UX

### 1.1 Text Input Mechanics (PasteAwareTextInput.tsx)

**Native behavior:**
- **Dual-value system**: `displayValue` (with placeholders) and `actualValue` (full content). Large pastes (>5 lines or >500 chars) get replaced with `[Pasted text #N +X lines]` placeholder; actual content preserved in registry.
- **Paste detection**: Bracketed paste events from Ink + heuristic detection of large additions (longest common prefix/suffix to find inserted segment).
- **Image paste**: Ctrl+V checks macOS clipboard for images; translates OSC 1337, data URLs, file paths; inserts placeholder.
- **Word navigation**: Option+Left/Right arrow moves by word boundary (skip whitespace, then word chars). Home/End keys supported (ESC[H/F, ESC[1~/4~, ESCOH/OF).
- **Forward delete**: fn+Delete (ESC[3~) deletes character AFTER cursor. Global timestamp coordination with ink-text-input to prevent double-delete.
- **Newline insertion**: Shift+Enter / Ctrl+Enter / Meta+Enter / Option+Enter (ESC+\r) all insert \n at cursor.
- **Escape stripping**: Lone ESC characters are stripped from input (they're control keys, not content).
- **Bash mode entry**: `!` on empty input is intercepted BEFORE state update (no flicker).
- **Backspace on empty**: Calls `onBackspaceAtEmpty` callback (exits bash mode).
- **Cursor management**: `caretOffsetRef` updated synchronously; `nudgeCursorOffset` state for one-shot cursor positioning; cleared after apply via setTimeout(0).

**zc current state:**
- Uses bubbletea textarea.Model — handles basic editing but:
  - NO paste placeholder system (multi-line paste goes straight into buffer)
  - NO image paste support
  - NO word navigation (Option+arrow)
  - NO forward delete (fn+Delete)
  - NO Home/End key support
  - Shift+Enter works (textarea handles it), but not Option+Enter
  - NO bash mode entry interception (we handle `!` in the server dispatch loop)
  - NO backspace-at-empty callback

**Gap severity: HIGH** — paste handling and cursor navigation are daily friction points.

### 1.2 Slash-Command Palette (InputRich.tsx + SlashCommandAutocomplete.tsx)

**Native behavior:**
- Triggered by `/` on empty buffer (or any buffer position? Need to verify).
- **Client-side fuzzy filtering** over pre-pushed command catalog.
- **Palette fills the input**: Selecting a command inserts `/<command> ` into the input with cursor at end; user adds args, then Enter submits.
- **Tab/Enter both select** the highlighted item.
- **Arrow navigation** up/down.
- **Backspace past `/` closes palette**.
- **Escape closes palette** and clears input.
- Catalog includes: builtin commands + custom commands + mod commands.
- Commands have: id, description, args_hint, routing class, source, hidden flag.

**zc current state:**
- `/` on empty buffer opens palette (correct).
- Client-side fuzzy filtering over pushed `command_catalog` (correct).
- **Palette fills the input** (correct — implemented in commit c20d184).
- Backspace past `/` closes palette (correct).
- Escape closes palette and clears input (correct).
- **Missing**: Tab selection (only Enter works? Need to verify).

**Gap severity: LOW** — mostly implemented, minor polish.

### 1.3 @ File Completion

**Native behavior:**
- Triggered by `@` in input.
- File list resolved **client-side** against current working directory (from `device_status.current_working_directory`).
- Fuzzy matching against filesystem.
- Selection inserts the path into the input.

**zc current state:**
- **NOT IMPLEMENTED** — no `@` completion in the Go client.

**Gap severity: MEDIUM** — nice-to-have, not critical.

### 1.4 Input Gating and Visual State

**Native behavior:**
- Input is disabled during streaming (no typing).
- Input is collapsed/hidden when approvals are pending or overlays are open.
- **Placeholder text rotates** through inspirational hints when empty (every 6 seconds).
- **Statusline transient hints** appear for: permission mode changes, queue mode changes, bash mode, etc. (3-second TTL).
- **Token count display** during streaming (smoothed, threshold-based).
- **Elapsed time display** during streaming.
- **Thinking message** rotates (random verb) during thinking phase.
- **Network phase indicator** (↑ upload, ↓ download, ⚠ error) in statusline.
- **Execution phase** drives spinner visual (requesting/thinking/toolUse/responding).
- **Context-usage tier** drives spinner width (wider as conversation fills).
- **Resize freeze**: Animations pause for 750ms during terminal resize to prevent flicker.

**zc current state:**
- Input disabled during streaming (turnActive gates it).
- Input visible during approvals (correct — we don't collapse, the modal overlays).
- Placeholder is static text ("Message the agent...").
- NO statusline transient hints.
- Token count shows in statusline (from device.usage).
- NO elapsed time display.
- Thinking message rotates (getRandomThinkingVerb) — IMPLEMENTED.
- Network phase indicator (↑↓⚠) — IMPLEMENTED.
- Execution phase drives spinner — IMPLEMENTED.
- NO context-usage tier spinner width variation.
- NO resize freeze (bubbletea v2 may handle this better).

**Gap severity: MEDIUM** — placeholder rotation and transient hints add polish.

---

## 2. TRANSCRIPT RENDERING AND ANIMATION UX

### 2.1 Streaming Text Rendering

**Native behavior:**
- Assistant messages stream token-by-token into the `lines` state.
- **Markdown is rendered incrementally** — but with a "safe boundary" heuristic (only re-render at blank lines, even code fence counts, no tables nearby).
- **Streaming prefix cache**: A `streamCache` (prefix → rendered output) avoids re-rendering the entire message on every token. Only the suffix after the last safe boundary is re-rendered.
- **Glamour markdown renderer** is used for finished messages; inline renderer for streaming.
- **User messages**: Styled with left border (colored), "you ▸" prefix, glamour-rendered text.
- **Assistant messages**: "agent" header with icon, glamour-rendered markdown. Continuation messages (same turn) omit the header.

**zc current state:**
- `transcript` frame pushes full `Line[]` every 50ms during streaming.
- Go client replaces transcript wholesale (`m.st.transcript = fr.Lines`).
- `lineItem` memoizes render by cache key (kind, text lengths, phase, etc.).
- Streaming assistant uses `streamMD` cache: prefix → rendered glamour output. Only re-renders the streaming entry.
- User messages: colored left border + "you ▸" + text (glamour for non-streaming).
- Assistant messages: "agent" header + glamour markdown.
- **Missing**: Safe boundary heuristic for streaming markdown (we re-render on every token change, which is fine for small updates but could be optimized).

**Gap severity: LOW** — our streaming render is functional, maybe slightly less efficient.

### 2.2 Tool Call Cards

**Native behavior:**
- **Per-tool-type rendering**: Edit/Write/MultiEdit use DiffRenderer; Bash uses SyntaxHighlightedCommand; Task uses SubagentGroupDisplay; Plan uses PlanRenderer; Todo uses TodoRenderer; etc.
- **Two-column layout**: 2-char gutter (dot or icon) + content.
- **Smart wrapping**: Function name and args kept together when possible.
- **Blinking dots** for pending/running states (BlinkDot component).
- **Result shown with └ prefix** underneath.
- **Colorized args**: Paths, filenames, labels, numbers get syntax highlighting.
- **Collapsed by default**: Tool output truncated to N lines; Ctrl+O expands.
- **Shell output**: Truncated at ingest (31 lines/6000B), but full content stored in buffers; expand shows more.
- **Precomputed diffs**: Cached from approval dialog for tool return rendering.
- **Tool call deferral**: Finished tool calls are deferred for TOOL_CALL_COMMIT_DEFER_MS (300ms) to batch rapid-fire tools.

**zc current state:**
- Tool cards render with icon + humanized name + params header, dim railed body (4 lines default).
- Ctrl+O expands.
- **Missing**: Per-tool-type specialized rendering (we have a generic tool card).
- **Missing**: Colorized args (syntax highlighting).
- **Missing**: Blinking dots for running state.
- **Missing**: Two-column layout with smart wrapping.
- **Missing**: Precomputed diffs from approval (we parse old_string/new_string from ArgsText for diffs, but not from approval dialog cache).
- **Missing**: Tool call deferral (no batching).

**Gap severity: HIGH** — tool cards are a major visual differentiator. Our generic cards lose scannability.

### 2.3 Reasoning Blocks

**Native behavior:**
- Reasoning messages render as "· reasoning (N chars — ctrl+r expands)" when collapsed.
- Expanded shows "· " prefix + reasoning text.
- Ctrl+R toggles expand/collapse.
- Reasoning tab cycling (if enabled): Tab cycles through reasoning variants (fable/fable-low/etc.).

**zc current state:**
- Reasoning shows "· reasoning (N chars — ctrl+r expands)" when collapsed.
- Expanded shows "· " + text.
- Ctrl+R toggles — IMPLEMENTED.
- NO reasoning tab cycling.

**Gap severity: LOW** — mostly implemented.

### 2.4 Static vs Live Items

**Native behavior:**
- **StaticTranscript**: Items that are "done" (finished tool calls, completed messages, command results) are moved to `staticItems`. They render in a "frozen" area that doesn't re-render on every frame (performance optimization).
- **liveItems**: Current turn's items (streaming assistant, pending approvals, running tools) render in the live area.
- **Commit logic**: Items move from live to static when they finish, with a deferral timer for tool calls.
- **Subagent grouping**: Finished Task tools are grouped into a `subagent_group` item.

**zc current state:**
- NO static/live split. All items render in the same list.
- NO subagent grouping (subagent deltas are dropped from transcript).
- **This is a significant architectural difference** — the native TUI uses static promotion for performance and visual stability.

**Gap severity: MEDIUM** — performance and visual stability suffer without static promotion.

### 2.5 Animations

**Native behavior:**
- **Spinner**: Multiple animation pools (MiniDot, Braille, etc.) selected by context-usage tier. Round-robin through pool on each stream start.
- **Shimmer text**: Agent name + thinking message shimmer during streaming.
- **Token smoothing**: Token count animates smoothly (not jumping).
- **Blinking dots**: For pending tool calls.
- **Compaction animation**: When context compacts.
- **Resize freeze**: 750ms pause during resize.

**zc current state:**
- Spinner: MiniDot only (no tier-based pool).
- NO shimmer text.
- NO token smoothing.
- NO blinking dots.
- NO compaction animation.
- NO resize freeze.

**Gap severity: MEDIUM** — animations add life and responsiveness feel.

---

## 3. APPROVAL AND TOOL-CALL UX

### 3.1 Approval Classification

**Native behavior:**
- Approvals classified by `classifyApprovals()`: auto-allowed, auto-denied, needs-user-input.
- `alwaysRequiresUserInput` flag forces AskUserQuestion to need input.
- `requireArgsForAutoApprove` ensures tool calls have complete args.
- Permission mode affects classification (standard vs acceptEdits vs unrestricted).
- Safe-read rules auto-approve safe reads in standard mode.

**zc current state:**
- Uses same `classifyApprovals()` function (imported from native helpers).
- Same permission mode logic.
- **Same behavior** — this is correctly delegated to native code.

**Gap severity: NONE** — correctly implemented.

### 3.2 Approval Dialog Rendering

**Native behavior:**
- **Inline rendering**: All approvals render inline in the transcript (no separate dialog overlay).
- **Per-tool-type UI**: Bash → InlineBashApproval; Edit/Write → InlineFileEditApproval; Memory → InlineMemoryApproval; Task → InlineTaskApproval; Question → InlineQuestionApproval; Generic → InlineGenericApproval.
- **ApprovalSwitch** dispatches to the right component based on tool name.
- **Diff previews**: Precomputed diffs from approval dialog are passed to tool return rendering.
- **Keyboard**: ↑/↓ navigate options, Enter selects, 1/2 number keys quick-select, Esc cancels, Ctrl+C cancels.
- **Custom deny**: Third option is a text input for "No, and tell Letta Code what to do differently".
- **Permission suggestions**: "Yes, and don't ask again for X" with scope selection (project/session).

**zc current state:**
- **Modal overlay** for approvals (not inline). This is a deliberate design choice — the Go client renders a modal over the transcript.
- **Generic approval UI only** — no per-tool-type specialized rendering.
- **Numbered selection**: Yes / Yes+don't-ask-again / No (matching native).
- **Keyboard**: ↑/↓, Enter, number keys, Esc, y/d/n — IMPLEMENTED.
- **NO custom deny text input**.
- **NO permission suggestion scope selection**.
- Diff previews in modal (from `diffs` field) — IMPLEMENTED.
- Diff previews in tool cards (from ArgsText) — IMPLEMENTED.

**Gap severity: HIGH** — inline rendering and per-tool-type UIs are major UX wins. Modal feels disconnected.

### 3.3 Sequential Approvals

**Native behavior:**
- Multiple approvals handled one at a time.
- **Pending stubs**: Undecided approvals show as stubs in the transcript.
- **Queued stubs**: Decided-but-not-yet-executed approvals show as stubs with decision indicator.
- **Approval queue**: `approvalResults` array tracks decisions; `currentApproval` is the active one.
- **Sets/Maps**: `pendingIds`, `queuedIds`, `approvalMap`, `stubDescriptions`, `queuedDecisions` for efficient rendering.

**zc current state:**
- Only one approval shown at a time (the head of `pendingApprovals`).
- NO stubs for pending/queued approvals.
- When one approval is answered, the next appears.
- **Much less informative** — user can't see how many approvals remain or what they are.

**Gap severity: HIGH** — sequential approval stubs are critical for multi-tool-turn UX.

### 3.4 AskUserQuestion

**Native behavior:**
- Renders as inline form with question text + options.
- Multi-select supported.
- Free-text input supported (no options).
- Answers submitted as `updated_input` on the approval response.

**zc current state:**
- Renders as a question form overlay (via `question.go` form component).
- Sequential field pages: select/multiselect/input/text/confirm.
- Answers sent as `approval_response` with `updated_input`.
- **Functional but different visual style**.

**Gap severity: LOW** — functional parity exists, just different rendering.

---

## 4. STATUSLINE, PANELS, AND AMBIENT UX

### 4.1 Statusline

**Native behavior:**
- **Right column**: Agent name · Model (truncated to fit). BYOK indicator (▲). Temporary model override indicator (▲).
- **Left column**: Mode hint (bash mode, permission mode, transient hints). Preemption hints ("Press CTRL-C again to exit").
- **Custom panels**: Mod panels with `order === 0` override the built-in statusline.
- **Built-in default**: Rendered when no custom panel is active and no preemption.
- **Transient hints**: 3-second TTL for mode changes, queue changes, etc.

**zc current state:**
- **Single statusline**: Mode pill · Agent name · Conversation title · ◇ model · status verb · tokens.
- Right side: "esc interrupt · shift+tab mode · ^g help".
- NO custom panel override (we render panels separately above input).
- NO transient hints.
- NO BYOK indicator.
- NO temporary model override indicator.

**Gap severity: MEDIUM** — our statusline is informative but lacks the native's richness.

### 4.2 Mod Panels

**Native behavior:**
- Panels registered via `letta.ui.openPanel({id, order, render})`.
- **Order semantics**: >1 = above input (highest at top); 1 = product-status row (under input); 0 = primary line (overrides statusline); <0 = below primary line.
- **Evaluated on every frame** (render function called with width and context).
- **Panel lines**: Array of strings returned by render function.

**zc current state:**
- Panels polled every 2 seconds via `mod_panels_query`.
- Server evaluates `renderModPanelLines()` with real context.
- Client places by order: >1 above input, 1 product-status, 0 primary, <0 below.
- **Same order semantics** — IMPLEMENTED.
- **Polling instead of per-frame** — acceptable trade-off for stdio transport.

**Gap severity: LOW** — polling is fine for stdio; order semantics match.

### 4.3 Notifications

**Native behavior:**
- **Focus-aware**: Bell + OSC 9 desktop notification when turn completes while window unfocused.
- **Notification hooks**: Run server-side at emit time.
- **Level**: info/warning/error.

**zc current state:**
- Focus-aware turn-complete notification (OSC9 + BEL) — IMPLEMENTED.
- NO notification hooks (server-side) — hooks run in the ui-server but we don't have a separate notification frame type.
- Toasts for errors/warnings — IMPLEMENTED.

**Gap severity: LOW** — mostly implemented.

### 4.4 Queue Display

**Native behavior:**
- Queued messages shown above input.
- Label: "queued" or "queued · defer".
- Each item shown with "↳" prefix.
- Press ↑ to edit queued message.

**zc current state:**
- Queue shown above input with "⋯ N queued" label.
- Each item with "↳" prefix.
- **NO "press ↑ to edit" hint**.
- **NO defer label**.

**Gap severity: LOW** — minor polish.

---

## 5. CONVERSATION LIFECYCLE AND SWITCHING UX

### 5.1 Startup Sequence

**Native behavior:**
- Backend mode detection (local vs cloud).
- Agent loading (from props or selection).
- Conversation selection (from props or creation).
- History backfill (if resuming).
- Mod loading.
- Session start hooks.
- Welcome screen with loading state.
- **Command hints** shown based on context (resuming? pinned? local backend? has messages?).

**zc current state:**
- Agentless lobby → agent picker → conversation picker → chat.
- History backfill via `getResumeDataFromBackend()` + `backfillBuffers()`.
- Mod loading via `createModAdapter()` + `reload()`.
- Session start hooks via `modAdapter.events.emit("conversation_open", ...)`.
- **NO welcome screen** — just "entering lobby…".
- **NO command hints**.

**Gap severity: MEDIUM** — welcome screen and hints help onboarding.

### 5.2 Conversation Switching

**Native behavior:**
- **Validation first**: Check conversation exists BEFORE updating state.
- **Clear transcript**: buffers cleared, static items cleared, emittedIds cleared.
- **Visual separator**: "─" separator between old and new conversation.
- **Backfill**: History loaded into static items with separator.
- **Pending approvals recovered**: If any, shown inline.
- **Success message**: "Resumed conversation with AgentName" + agent/conversation IDs.
- **Error handling**: 404 → "Conversation not found", 422 → "Invalid conversation ID".
- **Queued switch**: If agent busy, switch queued for after end_turn.

**zc current state:**
- `enterChat()` does validation + backfill + mod loading.
- Transcript replaced (not cleared with separator).
- Pending approvals recovered and pushed — IMPLEMENTED.
- **NO visual separator**.
- **NO success message** in transcript.
- **NO queued switch** — we don't support switching while a turn is active.
- Error handling: generic toast on failure.

**Gap severity: MEDIUM** — visual separator and success message add clarity.

### 5.3 Interrupt Handling

**Native behavior:**
- **Ctrl+C**: First press arms (2-second window), second press quits. During streaming: first press sends interrupt signal.
- **ESC**: If input has text, clears input. If input empty and turn active, sends interrupt. If overlay open, closes overlay.
- **Interrupt signal**: Sets `userCancelledRef`, increments `conversationGenerationRef`, aborts `abortControllerRef`.
- **Turn loop detects**: `currentAbortController.signal.aborted` → break loop.
- **Visual feedback**: "cancelling" status, spinner changes.

**zc current state:**
- Ctrl+C: First press arms (2s window), second press quits — IMPLEMENTED.
- ESC: If input has text, clears input. If input empty and turn active, sends `interrupt` frame — IMPLEMENTED.
- Interrupt fast-path in stdin handler aborts `currentAbortController` — IMPLEMENTED.
- Turn loop detects `.signal.aborted` — IMPLEMENTED.
- **Same behavior** — correctly implemented.

**Gap severity: NONE** — correctly implemented.

### 5.4 Queue Processing

**Native behavior:**
- `QueueRuntime` manages the queue with callbacks (onEnqueued, onDequeued, onBlocked, onCleared, onRemoved).
- Queue items flushed after turn ends (one per turn).
- **Defer mode**: When enabled, queued items only flush on `end_turn` (not on intermediate stops like tool calls).
- Queue items can be edited (↑ to edit).

**zc current state:**
- Simple array `messageQueue`.
- Flushed after turn ends via `drainMessageQueue()` — one per turn.
- **Defer mode**: `queueDeferred` flag; when true, items accumulate until `end_turn` — IMPLEMENTED.
- **NO queue editing**.
- **NO QueueRuntime** (not needed for our simpler model).

**Gap severity: LOW** — functional parity, missing edit.

---

## 6. CROSS-CUTTING ARCHITECTURAL DIFFERENCES

### 6.1 Static vs Live Transcript Split

The native TUI uses a **static/live split** for performance and visual stability:
- `staticItems`: Frozen, memoized, doesn't re-render.
- `liveItems`: Current turn's active content, re-renders on every frame.
- Commit logic moves items from live to static with deferral.

zc uses a **unified list** — all items re-render on every transcript push. This is simpler but less performant and visually less stable (items may shift as they finish).

**Recommendation**: Consider static promotion for tool calls and finished messages. This is a significant architectural change but would improve perceived performance.

### 6.2 Inline vs Modal Approvals

The native TUI renders approvals **inline in the transcript**. This keeps context — the user sees the tool call that needs approval right where it happened.

zc renders approvals as a **modal overlay**. This is simpler to implement but loses context — the user can't see the surrounding transcript while deciding.

**Recommendation**: Inline approvals are a major UX win. However, implementing them in the Go client would require significant restructuring of the transcript rendering. Consider as a Stage 3+ improvement.

### 6.3 Per-Tool-Type Rendering

The native TUI has **specialized renderers for each tool type**: DiffRenderer for edits, SyntaxHighlightedCommand for bash, PlanRenderer for plans, etc.

zc has a **generic tool card** for all tools. This loses scannability — users can't quickly distinguish a bash command from a file edit.

**Recommendation**: Implement per-tool-type rendering in the Go client. This is a high-impact, medium-effort improvement.

### 6.4 Paste Placeholder System

The native TUI's paste placeholder system (large pastes replaced with `[Pasted text #N +X lines]`) is a significant UX feature:
- Prevents input buffer pollution.
- Allows the user to see what was pasted.
- Resolves on submit.

zc has no equivalent — large pastes go straight into the textarea, which can make the input unwieldy.

**Recommendation**: Implement paste placeholder system. This is a high-impact, medium-effort improvement.

---

## 7. SUMMARY: GAPS RANKED BY SEVERITY

### HIGH SEVERITY (Major UX impact, daily friction)
1. **Paste placeholder system** — large pastes pollute input buffer.
2. **Per-tool-type rendering** — generic tool cards lose scannability.
3. **Inline approvals** — modal loses context; stubs missing for sequential approvals.
4. **Cursor navigation** — missing Option+arrow, Home/End, forward delete.
5. **Sequential approval stubs** — user can't see pending/queued approvals.

### MEDIUM SEVERITY (Noticeable polish gaps)
6. **Static/live transcript split** — performance and visual stability.
7. **Placeholder rotation** — static placeholder is boring.
8. **Transient hints** — mode/queue changes need visual feedback.
9. **Welcome screen** — startup feels bare.
10. **Visual separator on conversation switch** — clarity.
11. **Token smoothing** — token count jumps.
12. **Shimmer text** — adds life to streaming.
13. **@ file completion** — nice-to-have.

### LOW SEVERITY (Minor polish)
14. **Tab selection in palette** — Enter works, Tab would be nice.
15. **Queue editing** — ↑ to edit queued message.
16. **Defer label** — queue display missing defer indicator.
17. **BYOK indicator** — statusline missing provider indicator.
18. **Custom deny input** — approval modal missing text input for deny reason.
19. **Permission suggestion scope** — missing project/session selection.
20. **Context-usage tier spinner** — spinner width variation.

### NONE (Correctly implemented)
- Approval classification (delegated to native code).
- Interrupt handling.
- History backfill.
- Mod panel order semantics.
- Focus-aware notifications.
- Turn state machine (streaming/executing_tool/waiting_on_approval/idle).
- Network phase indicator.
- Execution phase spinner.
- Reasoning collapse/expand.
- Conversation resume with pending approvals.
- Queue defer mode.
- Permission mode cycling.

---

## 8. NEXT STEPS

This analysis should be cross-referenced with the subagent reports when they complete. The subagents may surface additional nuances I missed.

Priority order for implementation (Jack's call):
1. HIGH severity items first.
2. MEDIUM severity items next.
3. LOW severity items as time allows.

Each item should be traced back to the native source code (file + line number) for implementation reference.
