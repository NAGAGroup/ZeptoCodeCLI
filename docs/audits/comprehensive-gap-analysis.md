# zc UI Protocol: Comprehensive Native TUI Cross-Reference & Gap Analysis

> Synthesis of 5 parallel subagent deep-dives + direct source reading of letta-code 0.28.9
> Cross-referenced against zc's ui-server.ts (fork `zc-ui-server` branch) and Go client (`ZeptoCodeCLI` main)
> Focus: *HOW* the native TUI achieves UX quality, with specific code references for every behavior

## Document Structure

Each section follows this pattern:
1. **Native Behavior** — what the upstream TUI does, with file:line references
2. **zc Current State** — what we implement today
3. **Gap Assessment** — severity + specific missing pieces
4. **Implementation Notes** — where the behavior would live in zc (server vs client vs protocol)

---

## 1. INPUT / EDITING / SELECTION UX

### 1.1 Paste Handling (PasteAwareTextInput.tsx)

**Native behavior:**
- **Three-layer paste detection**: (1) bracketed paste `isPasted` flag from Ink, (2) Ctrl+V/Meta+V explicit clipboard check, (3) heuristic detection of large additions (>500 chars or >5 lines) via LCP/LCS segment extraction (`PasteAwareTextInput.tsx:192-348`, `596-691`)
- **Display/actual value split**: `displayValue` shows placeholder; `actualValue` holds full content. Large pastes replaced with `[Pasted text #N +X lines]` (`PasteAwareTextInput.tsx:267-280`)
- **Image paste**: Ctrl+V checks macOS clipboard via `tryImportClipboardImageMac()`; translates OSC 1337, data URLs, file paths; inserts placeholder (`PasteAwareTextInput.tsx:220-249`, `320-348`)
- **Small paste sanitization**: `sanitizeForDisplay()` replaces newlines with `↵` for inline display (`PasteAwareTextInput.tsx:59-61`)
- **Paste registry**: Global `allocatePaste()` / `resolvePlaceholders()` for placeholder resolution on submit (`PasteAwareTextInput.tsx:16-19`)

**zc current state:**
- Go client uses bubbletea `textarea.Model` — no paste detection at all
- Large pastes go straight into buffer, polluting input
- No image paste support
- No placeholder system

**Gap: HIGH** — daily friction for multi-line paste and image paste.

**Implementation:** Pure client-side (Go). No protocol changes needed. The `textarea` widget would need custom paste interception or replacement with a lower-level input component.

---

### 1.2 Cursor Navigation (PasteAwareTextInput.tsx)

**Native behavior:**
- **Option+Left/Right**: Word boundary navigation via `findPreviousWordBoundary` / `findNextWordBoundary` (`PasteAwareTextInput.tsx:64-105`, `384-400`)
- **Option+Delete**: Backward word delete (`PasteAwareTextInput.tsx:402-420`)
- **Forward delete (Fn+Delete)**: `forwardDeleteAtCursor()` + global timestamp `__lettaForwardDeleteTimestamp` to prevent ink-text-input double-delete (`PasteAwareTextInput.tsx:423-440`, `551-556`)
- **Home/End**: Detected via raw ANSI sequences `[H`, `[1~`, `OH` / `[F`, `[4~`, `OF` (`PasteAwareTextInput.tsx:528-545`)
- **All via raw stdin hook**: Uses Ink's private `internal_eventEmitter` for escape sequences `useInput` doesn't parse (`PasteAwareTextInput.tsx:381-594`)

**zc current state:**
- bubbletea `textarea` handles basic arrows, backspace, delete
- NO word navigation
- NO forward delete
- NO Home/End

**Gap: HIGH** — power users expect these keys.

**Implementation:** Pure client-side. bubbletea v2 may support some of this via `KeyMsg` with kitty protocol; otherwise need raw stdin handling.

---

### 1.3 Newline Insertion (PasteAwareTextInput.tsx)

**Native behavior:**
- **Shift/Ctrl/Meta+Enter**: Inserts `\n` at cursor (`PasteAwareTextInput.tsx:195-214`)
- **Option+Enter**: Detected via `\r` or `\n` raw sequences (`PasteAwareTextInput.tsx:505-507`)
- **VS Code style `\\r`**: Also treated as newline (`PasteAwareTextInput.tsx:513-516`)

**zc current state:**
- Shift+Enter works (bubbletea textarea handles it)
- Option+Enter NOT handled
- VS Code style NOT handled

**Gap: LOW** — Shift+Enter covers most cases.

---

### 1.4 History Recall (InputRich.tsx)

**Native behavior:**
- **Two-step boundary navigation**: Up on wrapped line → first press moves to start of first visual line, second press triggers history (`InputRich.tsx:1458-1491`)
- **Sticky column**: When navigating vertically through wrapped lines, cursor remembers preferred column (`InputRich.tsx:1142`)
- **Draft preservation**: `temporaryInput` preserves user's draft when entering history browsing (`InputRich.tsx:1135`)
- **Exit on type**: Typing while in history mode exits history but keeps modified text (`InputRich.tsx:1610-1617`)
- **Deduplication**: Identical consecutive entries not added (`InputRich.tsx:1647`, `1666`)

**zc current state:**
- Up/Down arrows recall history (`m.history` slice)
- Only works on single-line buffer (multi-line → cursor moves)
- NO draft preservation
- NO deduplication
- NO sticky column

**Gap: MEDIUM** — history is functional but rough around edges.

**Implementation:** Pure client-side. Extend `textarea` history or replace with custom implementation.

---

### 1.5 Slash-Command Palette (SlashCommandAutocomplete.tsx)

**Native behavior:**
- **Trigger**: `/` on any buffer position (not just empty) (`SlashCommandAutocomplete.tsx:258`)
- **Close on space**: Palette closes immediately if space after command (`SlashCommandAutocomplete.tsx:210-216`)
- **Filtering**: Substring matching (not fuzzy): `cmdName.includes(lowerQuery)` (`SlashCommandAutocomplete.tsx:227-229`)
- **Dynamic visibility**: `/pin` hidden if already pinned; `/unpin` hidden if not (`SlashCommandAutocomplete.tsx:138-156`)
- **Tab = autocomplete (fill only)**: `handleCommandAutocomplete` sets input to command, cursor at end (`InputRich.tsx:1724-1729`)
- **Enter = execute**: `handleCommandSelect` clears input, adds to history, calls `onSubmit(command)` (`InputRich.tsx:1699-1721`)
- **No-match fallback**: When no matches, autocomplete is NOT active, so Enter submits raw text (`SlashCommandAutocomplete.tsx:252-255`)
- **Scrolling window**: Max 8 rows, selected item kept in middle (`SlashCommandAutocomplete.tsx:282-288`)
- **Sources**: Built-in + mod + custom + skills, merged and deduplicated (`SlashCommandAutocomplete.tsx:167-185`)

**zc current state:**
- `/` on empty buffer opens palette (correct)
- Client-side filtering over pushed `command_catalog` (correct)
- Palette fills input on selection (correct, commit c20d184)
- **Missing**: Tab selection, close-on-space, dynamic visibility, no-match fallback, multi-source merging, scrolling window centering

**Gap: LOW-MEDIUM** — functional but missing polish.

**Implementation:** Pure client-side.

---

### 1.6 @ File Completion (FileAutocomplete.tsx)

**Native behavior:**
- **Trigger**: `@` in input takes precedence over `/` (`InputAssist.tsx:50-52`)
- **Data source**: `FileAutocompleteProvider` with `workingDirectory` + `fdPath` (fast file listing) (`FileAutocomplete.tsx:68-111`)
- **Async with cancellation**: `AbortController` support (`FileAutocomplete.tsx:68-111`)
- **Tab/Enter both apply**: `applyItem` replaces `@...` token with path (`FileAutocomplete.tsx:52-58`)
- **Visual**: 📁/📄 icons, directory color, dimmed descriptions, max 10 matches (`FileAutocomplete.tsx:117-150`)

**zc current state:**
- **NOT IMPLEMENTED**

**Gap: MEDIUM** — nice-to-have, not critical.

**Implementation:** Client-side filesystem reads against `device.cwd` from state. No protocol changes.

---

### 1.7 Bash Mode (InputRich.tsx)

**Native behavior:**
- **Entry**: `!` on empty input intercepted BEFORE state update — no flicker (`PasteAwareTextInput.tsx:598-603`)
- **Exit**: Backspace on empty bash input arms exit; second backspace exits (`InputRich.tsx:1205-1214`)
- **Disarm**: Typing after first backspace disarms (`InputRich.tsx:1233-1237`)
- **Visual**: Prompt changes to `⏵⏵ bash mode` in bash color (`InputRich.tsx:264-274`)

**zc current state:**
- `!` prefix routes to `runBash()` in server — no special bash mode UI
- No bash mode entry/exit mechanics
- No visual indication

**Gap: MEDIUM** — bash mode is a distinct UX pattern in the native TUI.

**Implementation:** Could be client-side (detect `!` prefix, show bash prompt) or server-side (push `bash_mode` state). Server-side is more consistent with our model.

---

### 1.8 Input Gating & Visual Feedback (InputRich.tsx)

**Native behavior:**
- **Divider color**: Top/bottom dividers dim when not in bash mode, brighten in bash mode (`InputRich.tsx:2017-2021`, `2056-2062`)
- **Prompt color**: Changes between normal and bash mode (`InputRich.tsx:2028-2029`)
- **Placeholder rotation**: 6-second cycle through inspirational hints when empty (`InputRich.tsx:75-81`, `1162-1189`)
- **Double-escape**: First Escape starts 2.5s timer; second clears input (`InputRich.tsx:1342-1357`)
- **Double-Ctrl-C**: First wipes input, starts 1s timer; second exits (`InputRich.tsx:1382-1404`)
- **Footer width freeze**: Right column width frozen during streaming to prevent edge jitter; shrink allowed if significant (`InputRich.tsx:1086-1130`)
- **Divider suppression during resize**: During terminal shrink, all dividers and footer chrome suppressed until width settles (`InputRich.tsx:1013-1025`, `1963-1972`)
- **Transient hints**: 3-second TTL for mode changes, queue changes, etc. (`InputRich.tsx:1058-1068`, `1758-1772`, `1800-1817`, `1819-1840`)
- **Agent info bar**: Below palette, shows agent name + model + reasoning effort (`InputAssist.tsx:92-99`)

**zc current state:**
- Input border color = permission mode (correct)
- Ctrl+C double-press works (correct)
- ESC clears input if non-empty; sends interrupt if empty (correct)
- **Missing**: Placeholder rotation, transient hints, divider color changes, footer width freeze, resize suppression, agent info bar, bash mode visuals

**Gap: MEDIUM** — polish items that add life to the UI.

**Implementation:** Mostly client-side. Some server-side state pushes (bash mode, transient hints) if we want them in the protocol.

---

## 2. TRANSCRIPT RENDERING & ANIMATION UX

### 2.1 Streaming Text Rendering

**Native behavior:**
- **Paragraph-level splitting**: `trySplitContent()` finds `\n\n` boundaries (not inside code fences) at 1500+ char minimum; commits "before" text as finished line, keeps "after" streaming (`accumulator.ts:853-908`, `markdown-split.ts:84-116`)
- **No visible cursor**: Text appears as chunks arrive; no blinking cursor at end (`accumulator.ts:1006`)
- **MarkdownDisplay**: Pure Ink components for headers, code blocks, lists, tables, etc. (`MarkdownDisplay.tsx`)
- **Inline markdown**: Bold, italic, inline code, links within paragraphs (`InlineMarkdownRenderer.tsx`)
- **Continuation lines**: `isContinuation` suppresses bullet on subsequent lines; 2-char gutter

**zc current state:**
- `transcript` frame pushes full `Line[]` every 50ms
- Go client replaces transcript wholesale (`m.st.transcript = fr.Lines`)
- `lineItem` memoizes render by cache key
- `streamMD` cache for streaming assistant: prefix → rendered glamour output
- **Missing**: Paragraph-level splitting (we re-render entire streaming entry), no continuation line suppression

**Gap: LOW** — our streaming is functional, maybe slightly less efficient.

**Implementation:** Client-side render optimization. No protocol changes.

---

### 2.2 Static vs Live Items Split

**Native behavior:**
- **`staticItems`**: Frozen in Ink's `<Static>` component — never re-renders. Committed via `commitEligibleLines()` (`AppCoordinator.tsx:2109-2280`)
- **`liveItems`**: Active React components that re-render on every state change
- **Commit rules**: User/error/status immediately; events/commands when `phase === "finished"`; assistant/reasoning when finished or via aggressive split; tool calls deferred 50ms (`TOOL_CALL_COMMIT_DEFER_MS`)
- **Task grouping**: Finished Task tools grouped into `subagent_group` item (`subagent-aggregation.ts:78-157`)
- **Performance**: Static area doesn't re-render — critical for long transcripts
- **Scroll stability**: Completed content "freezes"

**zc current state:**
- NO static/live split. All items render in unified list.
- NO subagent grouping (subagent deltas dropped from transcript)
- All items re-render on every transcript push

**Gap: HIGH** — this is a fundamental architectural difference affecting performance and visual stability.

**Implementation:** Client-side architecture change. Would require:
1. Tracking which items are "frozen" vs "live"
2. Separate render paths for frozen (memoized forever) vs live (re-render)
3. Protocol: no changes needed, but `transcript` push would need to indicate which items are new/changed

---

### 2.3 Tool Call Cards (ToolCallMessageRich.tsx)

**Native behavior:**
- **Two-column layout**: 2-char gutter + content (consistent across all message types)
- **Phase-based dots**: `streaming` = gray static; `ready`/`running` = blinking; `finished`+success = green; `finished`+error = red (`ToolCallMessageRich.tsx:351-368`)
- **BlinkDot**: 400ms toggle, respects `AnimationContext.shouldAnimate` (`BlinkDot.tsx:17-45`)
- **Smart args**: `formatArgsDisplay()` produces human-readable display; colorized paths/filenames/labels/numbers (`ToolCallMessageRich.tsx:45-88`)
- **Shell highlighting**: Shiki (Catppuccin Mocha) for bash grammar (`SyntaxHighlightedCommand.tsx:213-263`)
- **Tool-specific result renderers**: File read → line count; Write → `WriteRenderer`/`AdvancedDiffRenderer`; Edit → `EditRenderer`/`MultiEditRenderer`; Search → match count; Todo → checkboxes; Plan → checkboxes; Memory → `MemoryDiffRenderer`; Patch → `AdvancedDiffRenderer`; AskUserQuestion → Q&A format; Shell → `CollapsedOutputDisplay`
- **L-bracket prefix**: `└` for tool results (5 chars: `  └  `)
- **Streaming shell output**: `StreamingOutputDisplay` — rolling window of last 5 lines, elapsed timer, stderr in red, `(esc to interrupt)` hint (`StreamingOutputDisplay.tsx:18-97`)
- **Collapsed output**: First 3 lines, Ctrl+O expand, 300-char clip (`CollapsedOutputDisplay.tsx:25-127`)

**zc current state:**
- Generic tool card: icon + humanized name + params header, dim railed body (4 lines default)
- Ctrl+O expands
- **Missing**: Per-tool-type rendering, phase-based dots/blinking, colorized args, shell highlighting, tool-specific result renderers, L-bracket prefix, streaming shell output display, collapsed output with char clipping

**Gap: HIGH** — tool cards are a major visual differentiator. Our generic cards lose scannability.

**Implementation:** Client-side rendering. The protocol already carries `kind`, `name`, `argsText`, `resultText`, `phase`, `streaming` — enough to dispatch to specialized renderers.

---

### 2.4 User Message Rendering (UserMessageRich.tsx)

**Native behavior:**
- **Full-width background highlight**: Entire row gets background color extending to terminal edge; 4% black on light, 12% white on dark (`colors.ts:47-57`)
- **Blank padding rows**: One highlighted blank row above and below content
- **System reminder split**: `splitSystemReminderBlocks()` parses `<system-reminder>` tags; shown plain, user content highlighted (`UserMessageRich.tsx:141-196`)
- **Custom word wrap**: Respects full-width Unicode (`UserMessageRich.tsx:73-134`)

**zc current state:**
- Colored left border + "you ▸" + text
- Glamour render for non-streaming
- **Missing**: Full-width background, padding rows, system reminder split, custom word wrap

**Gap: LOW-MEDIUM** — our user messages are readable but less visually distinct.

**Implementation:** Client-side styling changes.

---

### 2.5 Reasoning Blocks (ReasoningMessageRich.tsx)

**Native behavior:**
- **Header**: `✻ Thinking…` (dimmed) on first line; skipped on continuation
- **Content**: Indented 2 spaces, `MarkdownDisplay` in `dimColor`
- **Section boundaries**: `normalizeReasoningSectionBoundaries()` inserts blank lines before `**Section Title**\n\n` (`accumulator.ts:914-916`)

**zc current state:**
- "· reasoning (N chars — ctrl+r expands)" when collapsed
- "· " + text when expanded
- Ctrl+R toggles
- **Missing**: Header on first line, section boundary normalization

**Gap: LOW** — mostly implemented.

---

### 2.6 Animations

**Native behavior:**
- **AnimationContext**: Global `shouldAnimate` flag; components consume via `useAnimation()` (`AnimationContext.tsx`)
- **Overflow detection**: Estimates live area height; disables animations when `estimatedLiveHeight >= terminalRows`; resumes with 2-line hysteresis (`AppCoordinator.tsx:4807-4830`)
- **BlinkDot**: 400ms toggle for running/ready tools (`BlinkDot.tsx:17-45`)
- **Braille spinner**: 90ms frame cycle (`BlinkingSpinner.tsx`)
- **Shimmer text**: 3-char highlight band shifts across text (`ShimmerText.tsx`)
- **Logo spin**: 75ms frame cycle, 16 frames (`AnimatedLogo.tsx`)
- **Compaction fan-out**: 30ms ticks, random garbage chars reveal left-to-right (`CompactingAnimation.tsx`)
- **Thinking shimmer**: Shimmer offset cycles during streaming (`InputRich.tsx`)
- **Token smoothing**: Piecewise-linear catch-up (+3 when close, 15% of gap when mid, +50 cap when far); snaps on stop (`use-token-smoothing.ts:23-68`)
- **Context-usage tier spinner**: Different pools, colors, sweep directions per `ExecutionPhase`; spinner width grows with context usage (`contextTierFromRatio`, `spinnerWidthForTier`)
- **Stream seed**: Round-robin through tier's pool on each stream start (`InputRich.tsx:672-678`)

**zc current state:**
- Spinner: MiniDot only (bubbletea built-in)
- NO shimmer text
- NO token smoothing
- NO blinking dots
- NO compaction animation
- NO context-usage tier variation
- NO stream seed rotation
- NO overflow detection

**Gap: MEDIUM** — animations add life and responsiveness feel.

**Implementation:** Client-side. bubbletea v2 has `spinner.Model` with multiple spinners; shimmer would need custom implementation.

---

### 2.7 Diff Rendering

**Native behavior:**
- **Simple diff**: `EditRenderer` (old red `-` / new green `+`), `MultiEditRenderer`, `WriteRenderer` (`DiffRenderer.tsx`)
- **Advanced diff**: Unified diff with hunks, context lines, line numbers, Shiki syntax highlighting, continuation rows (`AdvancedDiffRenderer.tsx:191-529`)
- **Memory diff**: Line-level `Diff.diffLines()`, word-level `Diff.diffWords()` for adjacent pairs, max 10 rows (`MemoryDiffRenderer.tsx:32-398`)
- **Colors**: Added `#213A2B`, removed `#4A221D`, word-added `#2d7a2d`, word-removed `#7a2d2d` (`colors.ts:249-263`)
- **Full-width background rows**: Each line padded to terminal width

**zc current state:**
- Diff in approval modal: colored +/- from `diffs` field
- Diff in tool cards: parses `old_string`/`new_string` from ArgsText
- **Missing**: Unified diff with hunks, context lines, line numbers, Shiki highlighting, word-level diff, full-width backgrounds, memory-specific diff rendering

**Gap: MEDIUM** — our diffs are functional but less informative.

**Implementation:** Client-side. The protocol already carries `diffs` and `argsText`.

---

### 2.8 Subagent Display (SubagentGroupDisplay.tsx)

**Native behavior:**
- **Tree structure**: `├─` / `└─` branch characters (`SubagentGroupDisplay.tsx`)
- **Header**: "Running N agents" with blinking dot / "Ran N agents" with green dot
- **Per-agent row**: Description (bold) · type · model · stats
- **Status**: "Launching…" / "Thinking" / "Running…" / "Done" / "Error"
- **Expand/collapse**: Ctrl+O toggles tool calls per agent
- **Condensed mode**: Simplified view when `shouldAnimate === false`
- **Static display**: `SubagentGroupStatic` — pure props, no hooks, frozen in `<Static>`

**zc current state:**
- Subagent deltas dropped from transcript (we filter them out)
- NO subagent display at all
- `turn.active_subagents` shows names in statusline

**Gap: HIGH** — subagent display is completely missing.

**Implementation:** Both sides. Server needs to push subagent state (we have `active_subagents` in `turn` but not the full tree). Client needs tree rendering.

---

### 2.9 Visual Hierarchy

**Native behavior:**
- **Spacing**: `marginTop={1}` between all live items (`AppView.tsx:570`)
- **Blank padding**: Highlighted blank rows in user messages
- **Horizontal rules**: `─` repeated (`MarkdownDisplay.tsx:364`)
- **Separator items**: `─` to terminal width (`StaticTranscript.tsx:81`)
- **Color scheme**: Accent `#8C8CF9`, success `#64CF64`, error `#F1689F`, warning `#FEE19C` (`colors.ts`)
- **Glyphs**: `•` bullet, `✻` asterisk, `└` L-bracket, `│` pipe, `⚠` warning, `!` bash, `◆` diamond (`CLI_GLYPHS`)

**zc current state:**
- Our own theme system (theme.go)
- Similar color philosophy but different palette
- **Missing**: Consistent glyph usage, separator items, marginTop spacing discipline

**Gap: LOW** — visual style is subjective; our theme is fine.

---

## 3. APPROVAL & TOOL-CALL UX

### 3.1 Approval Classification

**Native behavior:**
- 10-stage pipeline: cross-agent guard → deny rules → alwaysAsk → mode override → allow rules → read/glob/grep guard → session rules → settings rules → default (`src/permissions/checker.ts:138-445`)
- Three modes: `unrestricted`, `acceptEdits`, `standard` (`src/permissions/mode.ts:3-138`)
- `alwaysAsk` rules are unbypassable even in `unrestricted` mode
- `classifyApprovals()` auto-allows, auto-denies, or needs-user-input (`use-approval-flow.ts:735-987`)

**zc current state:**
- Uses same `classifyApprovals()` function (imported from native helpers)
- Same permission mode logic
- **Same behavior** — correctly delegated

**Gap: NONE**

---

### 3.2 Inline Approval Rendering (ApprovalSwitch.tsx)

**Native behavior:**
- **All approvals inline in transcript** — no separate dialog (`AppView.tsx:521-652`)
- **ApprovalSwitch dispatcher**: Routes to specialized component by tool type:
  - File edit/write/patch → `InlineFileEditApproval`
  - Shell → `InlineBashApproval`
  - AskUserQuestion → `InlineQuestionApproval`
  - Memory → `InlineFileEditApproval` (for edits) or `InlineMemoryApproval` (for delete/rename)
  - Task → `InlineTaskApproval`
  - Fallback → `InlineGenericApproval` (`ApprovalSwitch.tsx:366-512`)
- **Per-tool-type headers**: "Update `path`?", "Overwrite `path`?", "Write to `path`?", "Apply patch to N files?" (`InlineFileEditApproval.tsx:96-111`)
- **Diff previews**: Precomputed diffs from approval dialog passed to tool return rendering (`AppCoordinator.tsx:2387-2391`)

**zc current state:**
- **Modal overlay** for approvals — not inline
- **Generic approval UI only** — no per-tool-type rendering
- Diff previews in modal (from `diffs` field) — correct
- Diff previews in tool cards (from ArgsText) — correct

**Gap: HIGH** — inline rendering and per-tool-type UIs are major UX wins. Modal loses context.

**Implementation:** Client-side architectural change. Would require restructuring transcript rendering to embed approval UI inline.

---

### 3.3 Keyboard Shortcuts (InlineGenericApproval.tsx)

**Native behavior:**
- **Universal**: ↑/↓ navigate, Enter select, Esc cancel, Ctrl+C cancel all, 1/2 number keys quick-select
- **Custom deny**: Third option is inline text input — "No, and tell Letta Code what to do differently" (`InlineGenericApproval.tsx:78-79`)
- **When on custom option**: Enter submits denial reason, Esc clears text (or cancels if empty), all typing goes to custom text (`InlineGenericApproval.tsx:102-119`)
- **Memoized static content**: Tool content doesn't re-render when typing denial reason (`InlineGenericApproval.tsx:154-174`)

**zc current state:**
- Numbered selection: Yes / Yes+don't-ask-again / No
- ↑/↓, Enter, number keys, Esc, y/d/n — IMPLEMENTED
- **NO custom deny text input**
- **NO memoized static content** (our modal re-renders everything)

**Gap: MEDIUM** — custom deny is useful for steering the agent.

**Implementation:** Client-side. Add text input to approval modal.

---

### 3.4 Permission Suggestions (use-approval-flow.ts)

**Native behavior:**
- `analyzeToolApproval()` produces `recommendedRule` (`use-approval-flow.ts:759`)
- `savePermissionRule(rule, "allow", scope)` persists the rule (`use-approval-flow.ts:780`)
- For `Edit(**)` rule + session scope: switches UI permission mode to `acceptEdits` instead of saving rule (`use-approval-flow.ts:775`)
- `approveAlwaysText` is context-aware: "don't ask again for this project" vs "allow memory operations during this session" (`ApprovalSwitch.tsx:61-73`)
- After saving, **re-checks all remaining approvals** — if ALL would now be auto-allowed, batches them immediately (`use-approval-flow.ts:794-857`)

**zc current state:**
- Permission suggestions from `buildApprovalSuggestionPayload()` — IMPLEMENTED
- Selecting suggestion persists via `applySuggestedPermissionsForApproval()` — IMPLEMENTED
- **NO context-aware `approveAlwaysText`** (we show generic text)
- **NO re-check of remaining approvals** (we don't batch after permission save)

**Gap: MEDIUM** — the re-check batching is a nice UX optimization.

**Implementation:** Server-side. The `requestPermission` function would need to re-classify remaining approvals after a permission save.

---

### 3.5 Sequential Approval Stubs (AppCoordinator.tsx)

**Native behavior:**
- Only ONE approval shows full UI at a time (`pendingApprovals[approvalResults.length]`)
- **Pending stubs**: Undecided approvals show as "⧗ Awaiting approval: toolName" (`PendingApprovalStub.tsx:25-55`)
- **Queued stubs**: Decided-but-not-executed show as "✓ Decision queued: approve" or "✕ Decision queued: deny"
- **Sets/Maps**: `pendingIds`, `queuedIds`, `approvalMap`, `stubDescriptions`, `queuedDecisions` for efficient rendering (`AppCoordinator.tsx:651-743`)

**zc current state:**
- Only head of `pendingApprovals` shown
- NO stubs for pending/queued approvals
- User can't see how many remain or what they are

**Gap: HIGH** — sequential approval stubs are critical for multi-tool-turn UX.

**Implementation:** Client-side. Would need to render stubs alongside the active approval modal.

---

### 3.6 AskUserQuestion (InlineQuestionApproval.tsx)

**Native behavior:**
- **Header**: "Review plan" or question-specific header
- **Progress**: "Question 2 of 3" (`InlineQuestionApproval.tsx:320-325`)
- **Markdown detection**: Auto-detected if contains newlines or markdown patterns; rendered via `MarkdownDisplay` (`InlineQuestionApproval.tsx:33-49`)
- **Multi-select**: Space toggles checkbox; "Submit" is a separate selectable item; Enter on "Submit" confirms (`InlineQuestionApproval.tsx:72-88`, `221-261`)
- **Number keys**: 1-9 quick select/toggle (`InlineQuestionApproval.tsx:99-102`)
- **Custom text option**: Inline text input with cursor (`InlineQuestionApproval.tsx:103-117`)

**zc current state:**
- Question form overlay (via `question.go` form component)
- Sequential field pages: select/multiselect/input/text/confirm
- Answers sent as `approval_response` with `updated_input`
- **Functional but different visual style**

**Gap: LOW** — functional parity exists.

---

### 3.7 Approval Recovery (use-approval-flow.ts)

**Native behavior:**
- `recoverRestoredPendingApprovals()` called on resume (`use-approval-flow.ts:347`)
- Batch key from sorted `toolCallId`s prevents double-recovery (`approval-diffs.ts:237`)
- Checks for queued real results from previous session (`use-approval-flow.ts:254-284`)
- Re-analyzes each approval to rebuild contexts if not saved (`use-approval-flow.ts:191-224`)
- Desktop notification "Approval needed" on resume (`use-approval-flow.ts:289`)
- Stale approval handling: creates fresh denials with reason "Stale approval recovery: session resumed" (`use-queued-approval-submit.ts:54-101`)

**zc current state:**
- `getResumeDataFromBackend()` fetches pending approvals on enterChat
- Maps to `PendingApproval` wire shape with permission suggestions
- Pushes via `pushPendingApprovals()` — IMPLEMENTED
- **NO batch key deduplication**
- **NO desktop notification on resume**
- **NO stale approval handling** (we don't run before slash commands)

**Gap: MEDIUM** — recovery works but lacks edge-case handling.

**Implementation:** Server-side. The `enterChat` and `resumeRecoveredApproval` functions would need enhancement.

---

## 4. STATUSLINE, PANELS, AMBIENT UX

### 4.1 Statusline (InputRich.tsx + Default.tsx)

**Native behavior:**
- **Split layout**: Left (dynamic state) / Right (agent · model · indicators)
- **Right column**: Agent name (truncated ~40% width) · Model (remaining width) · BYOK indicator (▲) · Temporary override indicator (▲) (`Default.tsx:24-81`)
- **Left column**: Mode hint, bash mode, transient hints, preemption hints ("Press CTRL-C again to exit")
- **Custom panel override**: Mod panels with `order === 0` replace built-in statusline (`InputRich.tsx:503-525`)
- **Built-in default**: Rendered when no custom panel active and no preemption (`Default.tsx:83-105`)
- **ANSI-aware truncation**: `truncateToWidth()` strips escape codes before measuring (`formatting.ts:32`)
- **Token smoothing**: Piecewise-linear catch-up for token count display (`use-token-smoothing.ts:23-68`)
- **Shimmer animation**: 3-char highlight band sweeps across thinking message (`use-shimmer-animation.ts:23-85`)

**zc current state:**
- Single statusline: Mode pill · Agent name · Conversation title · ◇ model · status verb · tokens
- Right side: "esc interrupt · shift+tab mode · ^g help"
- NO custom panel override (we render panels separately above input)
- NO transient hints
- NO BYOK indicator
- NO temporary model override indicator
- NO token smoothing
- NO shimmer animation

**Gap: MEDIUM** — our statusline is informative but lacks native's richness.

**Implementation:** Client-side. Some server-side state (BYOK status, temporary override) would need to be pushed in `device` frame.

---

### 4.2 Mod Panels (ModPanelRow.tsx)

**Native behavior:**
- **Order semantics**: >1 = above input (highest at top); 1 = product-status row (under input); 0 = primary line (overrides statusline); <0 = below primary line (`ModPanelRow.tsx:25-46`)
- **Evaluated on every frame**: Render function called with width and context
- **Error handling**: Panel throws → returns `[]` (hidden, no crash) (`ModPanelRow.tsx:48-72`)
- **Max lines**: `MAX_MOD_PANEL_LINES = 8` (`ModPanelRow.tsx:17`)
- **Default product status**: Auto-injects if no user panel claims `order=1` (`product-status/default.ts:75-92`)

**zc current state:**
- Panels polled every 2 seconds via `mod_panels_query`
- Server evaluates `renderModPanelLines()` with real context
- Client places by order: >1 above input, 1 product-status, 0 primary, <0 below
- Same order semantics — IMPLEMENTED
- **Polling instead of per-frame** — acceptable for stdio
- **NO error handling** (panel errors may crash?)
- **NO max lines cap**
- **NO default product status panel**

**Gap: LOW** — polling is fine for stdio; minor gaps in error handling and defaults.

**Implementation:** Client-side for max lines cap and default panel. Server-side for error handling.

---

### 4.3 Terminal Title (TerminalTitleWriter.tsx)

**Native behavior:**
- **Configurable fields**: `app-name`, `project-name`, `current-dir`, `activity`, `run-state`, `agent-name`, `model-name`, `context-usage`, `token-count`, `elapsed-time`, `cost`, `version` (`window-title-config.ts`)
- **Default order**: `activity`, `agent-name`, `model-name`, `project-name`
- **Activity spinner**: 10Hz Braille animation when `hasActiveProgress` (`TerminalTitleWriter.tsx:43-148`)
- **Action required blink**: 1Hz toggle between `[ ! ]` and `[ . ]` when `requiresAction`
- **Memoized**: Only writes when title actually changes (`lastManagedTerminalTitleRef`)
- **Cleanup**: Resets title on unmount

**zc current state:**
- Static title: "zc — AgentName · ConversationTitle"
- **NO configurable fields**
- **NO activity spinner**
- **NO action required blink**

**Gap: LOW** — terminal title is nice-to-have.

**Implementation:** Client-side. Could read config from settings or use hardcoded defaults.

---

### 4.4 Queue Display (QueuedMessages.tsx)

**Native behavior:**
- **Above input**: `QueuedMessages` renders when `messageQueue.length > 0` (`QueuedMessages.tsx:12-55`)
- **Bullet style**: Immediate = `&gt;` bullet; Defer = `○` hollow bullet
- **Max 5 messages**: Overflow shows "...and N more"
- **User-only**: Filters to `kind === "user"`
- **Statusline hints**: "press ↑ to edit queued message · ctrl+d to hold queue until done" (`InputRich.tsx:1800-1817`)

**zc current state:**
- Queue shown above input with "⋯ N queued" label
- Each item with "↳" prefix
- **NO bullet style distinction**
- **NO max limit**
- **NO "press ↑ to edit" hint**
- **NO defer label**

**Gap: LOW** — functional, minor polish gaps.

**Implementation:** Client-side.

---

### 4.5 Help Dialog (HelpDialog.tsx)

**Native behavior:**
- **Two tabs**: Commands (all non-hidden + custom + mod) + Shortcuts (`HelpDialog.tsx:30-258`)
- **TabBar**: Active tab highlighted with `backgroundColor={colors.selector.itemHighlighted}` (`TabBar.tsx:15-38`)
- **Pagination**: PAGE_SIZE=10, `j`/`k` or `←`/`→` for page navigation
- **Dynamic loading**: `import("@/cli/commands/custom.js")` on mount (`HelpDialog.tsx:37`)
- **Page indicator**: "Page 2/3"
- **Footer**: "↑↓ scroll · ←→ page · Tab switch · Esc cancel"

**zc current state:**
- `/help` renders command list as plain text in a command line output
- **NO tabbed interface**
- **NO pagination**
- **NO shortcuts reference**
- **NO dynamic loading**

**Gap: MEDIUM** — help is a significant discoverability tool.

**Implementation:** Client-side overlay. Could reuse the selection overlay framework.

---

### 4.6 Overlay System (AppView.tsx + OverlayShell.tsx)

**Native behavior:**
- **Conditionally mounted**: `activeOverlay` state controls which overlay is visible (`AppView.tsx:~1600-1750`)
- **Modal capture**: All overlays capture keyboard input
- **No backdrop**: Renders inline over existing content
- **OverlayShell**: Consistent `&gt; /command` header + solid line + title + footer (`OverlayShell.tsx:23-56`)
- **Two-stage escape**: First Esc clears search/input; second Esc closes overlay
- **Only one at a time**: `activeOverlay` is single state value (`types.ts:55-82`)
- **Input hidden**: `anySelectorOpen` tells `Input` to collapse

**zc current state:**
- Selection overlay (`selection` frame) renders as modal over transcript
- Palette overlay (`/` trigger) renders as inline list above input
- **NO OverlayShell consistent header**
- **NO two-stage escape**
- **NO input collapse during overlay** (input stays visible behind modal)

**Gap: MEDIUM** — overlay consistency and input collapse would improve focus.

**Implementation:** Client-side.

---

### 4.7 Welcome Screen (WelcomeScreen.tsx)

**Native behavior:**
- **Two-column layout**: Animated logo (left) + info (right) (`WelcomeScreen.tsx:71-187`)
- **Loading states**: "assembling" → "importing" → "initializing" → "checking" → "ready" (`types.ts:26-30`)
- **Hints**: Command hints based on context (resuming? pinned? local backend?) (`AppCoordinator.tsx:250-326`)
- **Memfs warning**: If context repositories disabled, shows yellow warning (`WelcomeScreen.tsx`)
- **Animated logo**: Color-matched, animates during loading, stops when ready (`AnimatedLogo.tsx`)

**zc current state:**
- "entering lobby…" text when in lobby phase
- **NO loading screen**
- **NO command hints**
- **NO animated logo**
- **NO memfs warning**

**Gap: MEDIUM** — welcome screen helps onboarding and sets tone.

**Implementation:** Client-side. Could be a simple overlay in the lobby phase.

---

### 4.8 Selector Rendering (ModelSelector, ConversationSelector, AgentSelector)

**Native behavior:**
- **TabBar for categories**: Active tab highlighted (`TabBar.tsx`)
- **Sliding window pagination**: `startIndex = max(0, min(selected - visible + 1, total - visible))`
- **Search/filter**: Real-time typing filters; Esc clears first, then closes; Backspace removes filter chars
- **Keyboard**: ↑/↓/j/k navigate, ←/→/Tab switch category, Enter select, R refresh (ModelSelector)
- **Current item highlighting**: `&gt;` prefix + highlighted color; current model gets `(current)` suffix
- **Dynamic tab visibility**: Empty categories filtered out; recents only if ≥2 items
- **Progressive loading**: ConversationSelector shows list before message previews; ModelSelector shows cached handles immediately, refreshes background
- **Request deduplication**: `pendingResultsCache` prevents duplicate API calls
- **Mounted ref guards**: All async effects check `mountedRef.current`

**zc current state:**
- Selection overlay renders grouped items with header/rows
- ↑/↓ navigate, Enter select, Esc close
- **NO TabBar categories**
- **NO pagination**
- **NO search/filter**
- **NO progressive loading**
- **NO request deduplication**
- **NO mounted ref guards**

**Gap: MEDIUM** — selectors are functional but lack the native's sophistication.

**Implementation:** Client-side. The selection framework would need significant enhancement.

---

## 5. CONVERSATION LIFECYCLE & SWITCHING UX

### 5.1 Startup Sequence (AppCoordinator.tsx)

**Native behavior:**
- **Loading screen**: `WelcomeScreen` with progress states (`AppCoordinator.tsx:507-513`)
- **Model cache warming**: `prefetchAvailableModelHandles()` on mount (`AppCoordinator.tsx:368-370`)
- **Prop sync with ref guards**: Tracks previous prop values, only syncs when actually changed (`AppCoordinator.tsx:439-465`)
- **Backfill on ready**: `useLayoutEffect` fires: sets `hasBackfilledRef`, commits welcome snapshot, calls `backfillBuffers()`, appends status line, calls `refreshDerived()` and `commitEligibleLines()` (`AppCoordinator.tsx:2888-2989`)
- **Fresh session**: Separate `useEffect` commits welcome snapshot and status lines, waits for `agentProvenance` (`AppCoordinator.tsx:4884-4975`)
- **Command hints**: Context-aware hints based on resume/pinned/local-backend state (`AppCoordinator.tsx:250-326`)

**zc current state:**
- Agentless lobby → agent picker → conversation picker → chat
- History backfill via `getResumeDataFromBackend()` + `backfillBuffers()` — IMPLEMENTED
- Mod loading via `createModAdapter()` + `reload()` — IMPLEMENTED
- Session start hooks via `conversation_open` event — IMPLEMENTED
- **NO loading screen**
- **NO model cache warming**
- **NO command hints**
- **NO welcome snapshot**

**Gap: MEDIUM** — startup feels bare without the native's guidance.

**Implementation:** Client-side for loading screen and hints. Server-side for model cache warming (could push `models` frame proactively).

---

### 5.2 Conversation Resume (AppView.tsx)

**Native behavior:**
- **Validation before state change**: `getResumeDataFromBackend()` called BEFORE updating local state (`AppView.tsx:1215-1219`)
- **Atomic transcript reset**: Clears buffers, static items, emittedIds, context history, bootstrap state, deferred commits (`AppView.tsx:1265-1275`)
- **Visual separator**: `separator` static item inserted before backfilled messages (`AppView.tsx:1293-1301`)
- **Success message**: "Resumed conversation with **AgentName**" + agent/conversation IDs (`AppView.tsx:1240-1255`)
- **Pending approval recovery**: `recoverRestoredPendingApprovals()` called after transcript rebuild (`AppView.tsx:1315-1319`)
- **Error handling**: 404 → "Conversation not found", 422 → "Invalid conversation ID" (`AppView.tsx:1322-1336`)

**zc current state:**
- `enterChat()` does validation + backfill + mod loading — IMPLEMENTED
- Transcript replaced (not cleared with separator)
- Pending approvals recovered — IMPLEMENTED
- **NO visual separator**
- **NO success message** in transcript
- Error handling: generic toast

**Gap: MEDIUM** — visual separator and success message add clarity.

**Implementation:** Client-side for separator and success message. Server-side for better error messages.

---

### 5.3 Conversation Switching (AppView.tsx + useConversationSwitching.ts)

**Native behavior:**
- **Busy guard with queueing**: If `isAgentBusy()`, switch queued as `QueuedOverlayAction` for after `end_turn` (`AppView.tsx:1180-1197`, `useConversationSwitching.ts:527-546`)
- **Session persistence**: `settingsManager.persistSession(agentId, convId)` immediately on success (`AppView.tsx:1237`)
- **Switch context**: `pendingConversationSwitchRef` set with origin metadata for next message reminder (`conversation-switch-alert.ts:34-91`)
- **Model carryover**: `maybeCarryOverActiveConversationModel()` copies current model override (`useConversationSwitching.ts:372`, `AppCoordinator.tsx:3450-3488`)
- **Same patterns for**: `/resume` selector, `/search` result click, `/agents` agent switch, `/btw` jump

**zc current state:**
- `selection_choice` with tag "conversation" triggers `enterChat()` — IMPLEMENTED
- **NO busy guard / queueing**
- **NO session persistence** (we don't call `settingsManager.persistSession`)
- **NO switch context / reminder injection**
- **NO model carryover**

**Gap: MEDIUM** — switching works but lacks the native's safety nets.

**Implementation:** Server-side for busy guard, persistence, model carryover. Client-side for switch context (would need new protocol frame or server-side injection).

---

### 5.4 New Conversation (AppView.tsx)

**Native behavior:**
- **Backend creation**: `getBackend().createConversation({ agent_id: agentId })` (`AppView.tsx:1360`)
- **Model carryover**: `maybeCarryOverActiveConversationModel()` (`AppView.tsx:1364`)
- **Fresh state**: Same atomic reset as resume, but `setConversationAutoTitleEligibility(true)` enables auto-title (`AppView.tsx:1368`)
- **Bootstrap reminders**: `resetBootstrapReminderState(true)` ensures first message gets bootstrap (`AppView.tsx:1397`)
- **Success message**: "Started new conversation with **AgentName**" (`AppView.tsx:1373-1382`)

**zc current state:**
- `/new` creates conversation via `backend.createConversation()` — IMPLEMENTED
- `enterChat()` with "new" — IMPLEMENTED
- **NO model carryover**
- **NO auto-title eligibility**
- **NO bootstrap reminders**
- **NO success message**

**Gap: MEDIUM** — new conversation lacks the native's "fresh but familiar" feel.

**Implementation:** Server-side for model carryover, auto-title, bootstrap. Client-side for success message.

---

### 5.5 Interrupt Handling (use-interrupt-handler.ts)

**Native behavior:**
- **EAGER_CANCEL** (default true): Immediate client-side abort without waiting for backend (`use-interrupt-handler.ts:227-229`)
- **Generation bump**: `conversationGenerationRef.current += 1` marks in-flight `processConversation` as stale (`use-interrupt-handler.ts:270-272`)
- **State reset**: `processingConversationRef = 0`, `abortControllerRef = null`, `setStreaming(false)`, `setIsExecutingTool(false)` — all synchronous (`use-interrupt-handler.ts:275-282`)
- **Backend cancel**: Fire-and-forget `getBackend().cancelConversation(...)` (`use-interrupt-handler.ts:327-342`)
- **Approval caching**: Pending approvals, auto-handled results, auto-denied results queued as denials (`use-interrupt-handler.ts:289-318`)
- **Interrupt recovery**: `pendingInterruptRecoveryConversationIdRef` gates `INTERRUPT_RECOVERY_ALERT` injection on next message (`use-conversation-loop.ts:658-693`)

**zc current state:**
- Ctrl+C: First press arms (2s window), second press quits — IMPLEMENTED
- ESC: If input empty and turn active, sends `interrupt` frame — IMPLEMENTED
- Interrupt fast-path aborts `currentAbortController` — IMPLEMENTED
- Turn loop detects `.signal.aborted` — IMPLEMENTED
- **NO generation bump** (we don't have generation tracking)
- **NO backend cancel** (we don't call `backend.cancelConversation`)
- **NO approval caching on interrupt**
- **NO interrupt recovery alert**

**Gap: MEDIUM** — interrupt works but lacks edge-case handling.

**Implementation:** Server-side for backend cancel, approval caching, interrupt recovery.

---

### 5.6 ESC Handling (InputRich.tsx + use-interrupt-handler.ts)

**Native behavior:**
- **During streaming/execution**: Calls `handleInterrupt()` — same as Ctrl+C (`use-interrupt-handler.ts:123`)
- **During queue cancel**: If `waitingForQueueCancelRef`, ESC sets `restoreQueueOnCancel = true` (`use-interrupt-handler.ts:232-234`)
- **During connect operation**: `onEscapeCommandCancel` cancels active connect (`AppCoordinator.tsx:5006-5012`)
- **During profile confirmation**: `handleProfileEscapeCancel` dismisses dialog (`AppCoordinator.tsx:4634-4641`)
- **Overlay dismissal**: If overlay open, ESC closes it
- **Input clear**: If input has text, clears input

**zc current state:**
- ESC: If input has text, clears input. If input empty and turn active, sends interrupt. — IMPLEMENTED
- **NO queue cancel restore**
- **NO connect operation cancel**
- **NO profile confirmation dismiss**
- **NO overlay dismissal via ESC** (overlays have their own Esc handling)

**Gap: LOW-MEDIUM** — ESC is mostly correct, missing some context-awareness.

**Implementation:** Client-side for overlay dismissal. Server-side for queue cancel restore.

---

### 5.7 Queue Processing (AppCoordinator.tsx)

**Native behavior:**
- **Dequeue effect**: `useEffect` watches `dequeueEpoch`, gates on multiple conditions (`AppCoordinator.tsx:4338-4436`):
  - `!streaming && !commandRunning && !isExecutingTool`
  - `pendingApprovals.length === 0`
  - `!queuedOverlayAction`
  - `!anySelectorOpen`
  - `!waitingForQueueCancelRef`
  - `!userCancelledRef`
  - `!abortControllerRef`
  - `!dequeueInFlightRef` (at-most-once lock)
  - **Defer mode**: Only dequeues when `lastStopReasonRef.current === "end_turn"` AND `processingConversationRef.current === 0`
- **Batch consumption**: `tuiQueueRef.current?.consumeItems(queueLen)` returns all items at once (`AppCoordinator.tsx:4365`)
- **Mode reset**: After dequeue, `setQueueMode("immediate")` (`AppCoordinator.tsx:4396`)
- **Error restoration**: `lastDequeuedMessageRef.current` restored to input on error (`AppCoordinator.tsx:4388`)
- **Queue clear on error**: `tuiQueueRef.current?.clear("error")` drops pending messages (`use-conversation-loop.ts:2672`)

**zc current state:**
- Simple array `messageQueue`
- `drainMessageQueue()` flushes after turn ends — one per turn
- `queueDeferred` flag for defer mode — IMPLEMENTED
- **NO dequeue effect with multiple guards**
- **NO batch consumption**
- **NO mode reset after dequeue**
- **NO error restoration**
- **NO queue clear on error**

**Gap: MEDIUM** — queue works but lacks safety nets.

**Implementation:** Server-side. The `processTurn` and `drainMessageQueue` functions would need enhancement.

---

### 5.8 Post-Turn Reflection (post-turn-reflection.ts + reflection-launcher.ts)

**Native behavior:**
- **Trigger evaluation**: `maybeRunPostTurnReflection()` at `end_turn` after transcript delta appended (`use-conversation-loop.ts:1488`)
- **Gate conditions**: `memfsEnabled` and valid `agentId` (`post-turn-reflection.ts:33`)
- **Two trigger modes**: `compaction-event` (immediate) and `step-count` (reads `steps_since_last_successful_reflection` from transcript) (`post-turn-reflection.ts:50-74`)
- **Reflection launcher**: `launchReflectionSubagent()` spawns background task via `spawnBackgroundSubagentTask()` (`reflection-launcher.ts:492-692`)
  - Reserves launch with `tryReserveReflectionLaunch()` to prevent duplicates
  - Builds reflection payload from unreflected transcript range
  - Creates memory worktree for isolation
  - On completion: finalizes worktree, recompiles parent agent's system prompt, emits task notification
- **Reflection arena** (experiment): Two reflection candidates with different models; user chooses (`reflection-arena.ts:644-841`)

**zc current state:**
- `/reflect` command launches reflection subagent manually — IMPLEMENTED
- **NO automatic post-turn reflection**
- **NO step-count trigger**
- **NO compaction-event trigger**
- **NO reflection arena**

**Gap: MEDIUM** — automatic reflection is a key "invisible maintenance" feature.

**Implementation:** Server-side. Would need to hook into the turn end in `processTurn`.

---

### 5.9 Session End (AppCoordinator.tsx + session.ts)

**Native behavior:**
- **Session save**: `saveLastSessionBeforeExit()` persists agent + conversation (`session.ts:8-24`)
- **SessionEnd hooks**: `runEndHooks()` with duration, message count, tool call count (`AppCoordinator.tsx:1160-1196`)
- **Mod event**: `conversation_close` with stats (`AppCoordinator.tsx:1178-1189`)
- **Telemetry flush**: `telemetry.trackSessionEnd()` and `telemetry.flush()` (`AppCoordinator.tsx:3928-3955`)
- **Local history**: `recordSessionEnd()` writes to local history file (`AppCoordinator.tsx:3933-3952`)
- **Exit stats**: `setShowExitStats(true)` renders summary; `setTimeout(() => process.exit(0), 100)` gives React time to paint (`AppCoordinator.tsx:3957-3961`)

**zc current state:**
- `rl.on("close")` emits `conversation_close` event — IMPLEMENTED
- **NO session save**
- **NO SessionEnd hooks**
- **NO telemetry flush**
- **NO local history**
- **NO exit stats screen**

**Gap: MEDIUM** — session end feels abrupt without summary.

**Implementation:** Client-side for exit stats. Server-side for hooks, telemetry, history.

---

### 5.10 Crash Recovery / Stream Errors (use-conversation-loop.ts)

**Native behavior:**
- **Pre-stream errors** (lines 797-1215):
  - Stale approval recovery: auto-denies stale approvals and retries (`use-conversation-loop.ts:828-862`)
  - Conversation busy (409): stream resume via `drainStreamWithResume`; fallback to wait/retry with exponential backoff (`use-conversation-loop.ts:865-1006`)
  - Transient errors (429/5xx): retries up to 3 with provider fallback to Bedrock after attempt 2 (`use-conversation-loop.ts:1011-1115`)
- **Post-stream errors** (lines 2245-2832):
  - Invalid tool call IDs: fetches real pending approvals from backend and restores them (`use-conversation-loop.ts:2265-2353`)
  - Approval pending: auto-denies stale approvals and retries (`use-conversation-loop.ts:2359-2394`)
  - Quota limit: auto-switches to `letta/auto` model temporarily (`use-conversation-loop.ts:2396-2448`)
  - Empty response: retries with system reminder nudge on last attempt (`use-conversation-loop.ts:2450-2503`)
  - Retriable errors: retries with fresh OTIDs, resets `highestSeqIdSeen` (`use-conversation-loop.ts:2506-2635`)
- **Final error display**: Formatted error with run_id, stop_reason, hint line (`use-conversation-loop.ts:2649-2832`)
- **Input restoration**: `lastDequeuedMessageRef.current` restored to input (`use-conversation-loop.ts:2667-2669`, `2821-2823`)
- **Queue clear on error**: `tuiQueueRef.current?.clear("error")` (`use-conversation-loop.ts:2672`)

**zc current state:**
- Basic error handling: `writeFrame({ type: "error", message: ... })` on stream failure
- `pushTurn("idle", { stop_reason: "error" })`
- **NO stale approval recovery**
- **NO conversation busy retry**
- **NO transient error retry**
- **NO provider fallback**
- **NO quota auto-switch**
- **NO empty response retry**
- **NO input restoration**
- **NO queue clear on error**

**Gap: HIGH** — error recovery is a major resilience feature.

**Implementation:** Server-side. The `processTurn` function would need significant enhancement.

---

### 5.11 Reasoning Cycle (use-reasoning-cycle.ts)

**Native behavior:**
- **Tab key**: Cycles through reasoning tiers (none → minimal → low → medium → high → xhigh → max) (`use-reasoning-cycle.ts:500-502`)
- **Optimistic UI**: Footer model display updates immediately via `setLlmConfig()` (`use-reasoning-cycle.ts:500-502`)
- **Agent state patch**: Local `agentState` patched optimistically so footer doesn't snap back (`use-reasoning-cycle.ts:504-548`)
- **Debounce flush**: 500ms timer batches rapid tab presses; reschedules if agent busy (`use-reasoning-cycle.ts:216-224`)
- **Server update**: `flushPendingReasoningEffort()` calls `updateAgentLLMConfig` or `updateConversationLLMConfig` (`use-reasoning-cycle.ts:235-343`)
- **Revert on failure**: Last confirmed config snapshot restored (`use-reasoning-cycle.ts:363-390`)

**zc current state:**
- **NO reasoning tab cycling**
- **NO optimistic UI**
- **NO debounce flush**

**Gap: LOW** — reasoning tab cycling is a power-user feature.

**Implementation:** Client-side for Tab key handling. Server-side for config update.

---

## 6. PROTOCOL GAPS

### 6.1 Missing Frame Types

The following native state pieces are NOT represented in our protocol (`protocol.ts`):

| Native State | Our Protocol | Gap |
|--------------|-------------|-----|
| `staticItems` vs `liveItems` | Unified `transcript` | No static/live split |
| `expandedToolCallId` | Not pushed | Client tracks locally |
| `lastShellToolCallId` | Not pushed | Client tracks locally |
| `hasBackfilledRef` | Not pushed | Client infers from transcript |
| `conversationGenerationRef` | Not pushed | No generation tracking |
| `processingConversationRef` | Not pushed | No processing lock tracking |
| `userCancelledRef` | Not pushed | No cancel tracking |
| `lastStopReasonRef` | Not pushed | No stop reason tracking |
| `dequeueEpoch` | Not pushed | No queue epoch tracking |
| `lastDequeuedMessageRef` | Not pushed | No input restoration |
| `pendingInterruptRecoveryConversationIdRef` | Not pushed | No interrupt recovery |
| `trajectoryTokenBase` / `trajectoryElapsedBaseMs` | Not pushed | No trajectory stats |
| `tokenStreamingEnabled` | Not pushed | No streaming toggle |
| `showCompactionsEnabled` | Not pushed | No compaction toggle |
| `reasoningTabCycleEnabled` | Not pushed | No reasoning cycle toggle |
| `currentSystemPromptId` | Not pushed | No system prompt tracking |
| `currentPersonalityId` | Not pushed | No personality tracking |
| `currentToolsetPreference` | Not pushed | No toolset tracking |
| `hasTemporaryModelOverride` | Not pushed | No override indicator |
| `billingTier` | Not pushed | No billing info |
| `updateNotification` | Not pushed | No update notification |
| `footerUpdateText` | Not pushed | No footer notifications |
| `searchQuery` | Not pushed | No search state |
| `modelSelectorOptions` | Not pushed | No selector options state |
| `modelReasoningPrompt` | Not pushed | No reasoning prompt state |
| `profileConfirmPending` | Not pushed | No profile confirmation |
| `worktreeDiffSelectorPending` | Not pushed | No worktree diff state |
| `reflectionArenaChoicePending` | Not pushed | No arena choice state |
| `queuedOverlayAction` | Not pushed | No queued overlay actions |
| `restoreQueueOnCancel` | Not pushed | No queue cancel restore |
| `lastSentInputRef` | Not pushed | No input restoration |
| `approvalToolContextIdRef` | Not pushed | No tool context tracking |
| `llmApiErrorRetriesRef` | Not pushed | No retry tracking |
| `conversationBusyRetriesRef` | Not pushed | No busy retry tracking |
| `emptyResponseRetriesRef` | Not pushed | No empty response retry |
| `quotaAutoSwapAttemptedRef` | Not pushed | No quota swap tracking |
| `providerFallbackAttemptedRef` | Not pushed | No fallback tracking |
| `shouldAutoGenerateConversationTitleRef` | Not pushed | No auto-title tracking |
| `isAutoConversationTitleInFlightRef` | Not pushed | No title in-flight |
| `shouldAutoGenerateConversationDescriptionRef` | Not pushed | No description tracking |
| `isAutoConversationDescriptionInFlightRef` | Not pushed | No description in-flight |
| `firstUserQueryRef` | Not pushed | No first query tracking |
| `sharedReminderStateRef` | Not pushed | No reminder state |
| `_systemPromptRecompileByConversationRef` | Not pushed | No recompile tracking |
| `_queuedSystemPromptRecompileByConversationRef` | Not pushed | No queued recompile |
| `modContext` (full) | Partial in `device` | Missing many fields |

**Assessment**: Most of these are internal state that doesn't need to be in the protocol. The key missing pieces are:
- `staticItems` vs `liveItems` split (architectural)
- `modContext` completeness (for panel rendering)
- Trajectory stats (for statusline)

---

### 6.2 Missing Client → Server Events

| Native Event | Our Protocol | Gap |
|--------------|-------------|-----|
| `expand_tool_call` (ctrl+o) | Not sent | Client tracks locally |
| `cycle_reasoning_effort` (tab) | Not sent | No reasoning cycle |
| `edit_queued_message` (↑ on queue) | Not sent | No queue editing |
| `clear_queue` (ctrl+d in defer) | `queue_defer_set` only | No explicit clear |
| `cancel_connect` (esc during connect) | Not sent | No connect cancel |
| `dismiss_profile_confirm` (esc) | Not sent | No profile confirmation |
| `title_preview` / `title_preview_end` | Not sent | No title preview |
| `feedback_prefill` | Not sent | No feedback prefill |
| `search_query_change` | Not sent | No search query stream |
| `model_selector_filter` | Not sent | No selector filter stream |
| `agent_selector_filter` | Not sent | No selector filter stream |
| `conversation_selector_filter` | Not sent | No selector filter stream |
| `scroll_position` | Not sent | No scroll tracking |
| `focus_change` | Not sent | No focus tracking |
| `resize_complete` | Not sent | No resize tracking |

**Assessment**: Most of these are client-local events that don't need server round-trips. The key missing pieces are reasoning cycle and queue editing.

---

## 7. SUMMARY: GAPS RANKED BY SEVERITY & EFFORT

### CRITICAL (Breaks daily workflow)
| # | Gap | Severity | Effort | Where |
|---|-----|----------|--------|-------|
| 1 | **Paste placeholder system** | HIGH | Medium | Client |
| 2 | **Per-tool-type rendering** | HIGH | High | Client |
| 3 | **Inline approvals** | HIGH | High | Client |
| 4 | **Sequential approval stubs** | HIGH | Medium | Client |
| 5 | **Cursor navigation** (Option+arrow, Home/End, forward delete) | HIGH | Medium | Client |
| 6 | **Static/live transcript split** | HIGH | High | Client |
| 7 | **Crash recovery / error retry** | HIGH | High | Server |

### IMPORTANT (Noticeable friction)
| # | Gap | Severity | Effort | Where |
|---|-----|----------|--------|-------|
| 8 | **Subagent display** | HIGH | Medium | Both |
| 9 | **Tool card polish** (dots, colorized args, shell highlighting, L-bracket) | MEDIUM | Medium | Client |
| 10 | **Queue safety nets** (guards, error restore, clear on error) | MEDIUM | Medium | Server |
| 11 | **Interrupt edge cases** (backend cancel, approval cache, recovery alert) | MEDIUM | Medium | Server |
| 12 | **Conversation switching safety** (busy guard, queueing, model carryover) | MEDIUM | Medium | Server |
| 13 | **Welcome screen / startup guidance** | MEDIUM | Medium | Client |
| 14 | **Help dialog** (tabs, pagination, shortcuts) | MEDIUM | Medium | Client |
| 15 | **Statusline richness** (transient hints, BYOK indicator, token smoothing) | MEDIUM | Low | Client |
| 16 | **Animations** (blinking dots, shimmer, token smoothing, overflow detection) | MEDIUM | Medium | Client |
| 17 | **Diff rendering** (unified diff, context lines, word-level highlighting) | MEDIUM | Medium | Client |
| 18 | **Custom deny input** | MEDIUM | Low | Client |
| 19 | **Permission suggestion re-check** | MEDIUM | Low | Server |
| 20 | **Auto-reflection** | MEDIUM | Medium | Server |
| 21 | **Session end summary** | MEDIUM | Low | Client |
| 22 | **New conversation polish** (model carryover, auto-title, bootstrap) | MEDIUM | Medium | Server |
| 23 | **Visual separator on switch** | MEDIUM | Low | Client |
| 24 | **Success messages** (resume, new, switch) | MEDIUM | Low | Client |
| 25 | **Placeholder rotation** | MEDIUM | Low | Client |

### NICE-TO-HAVE (Polish)
| # | Gap | Severity | Effort | Where |
|---|-----|----------|--------|-------|
| 26 | **@ file completion** | LOW | Medium | Client |
| 27 | **Bash mode UI** | LOW | Medium | Client/Server |
| 28 | **Terminal title customization** | LOW | Low | Client |
| 29 | **Queue editing** (↑ to edit) | LOW | Low | Client |
| 30 | **Defer label in queue** | LOW | Low | Client |
| 31 | **Tab selection in palette** | LOW | Low | Client |
| 32 | **Close-on-space in palette** | LOW | Low | Client |
| 33 | **Dynamic command visibility** | LOW | Low | Client |
| 34 | **No-match fallback in palette** | LOW | Low | Client |
| 35 | **Scrolling window centering** | LOW | Low | Client |
| 36 | **Agent info bar** | LOW | Low | Client |
| 37 | **Divider suppression during resize** | LOW | Low | Client |
| 38 | **Footer width freeze** | LOW | Low | Client |
| 39 | **History draft preservation** | LOW | Low | Client |
| 40 | **History deduplication** | LOW | Low | Client |
| 41 | **Sticky column in wrapped lines** | LOW | Low | Client |
| 42 | **Reasoning tab cycling** | LOW | Low | Client/Server |
| 43 | **Context-usage tier spinner** | LOW | Low | Client |
| 44 | **Stream seed rotation** | LOW | Low | Client |
| 45 | **Compaction animation** | LOW | Medium | Client |
| 46 | **User message full-width highlight** | LOW | Low | Client |
| 47 | **System reminder split** | LOW | Low | Client |
| 48 | **Selector enhancements** (categories, pagination, search, progressive loading) | LOW | High | Client |
| 49 | **Overlay consistency** (OverlayShell, two-stage escape, input collapse) | LOW | Medium | Client |
| 50 | **Mod panel error handling** | LOW | Low | Server |
| 51 | **Default product status panel** | LOW | Low | Server |
| 52 | **Max panel lines cap** | LOW | Low | Client |

---

## 8. ARCHITECTURAL RECOMMENDATIONS

### 8.1 Client-Side Architecture

The native TUI's biggest UX wins come from **client-side richness**:
- Inline approvals (not modal)
- Per-tool-type rendering
- Static/live split
- Animation system
- Input editing sophistication

These are all **rendering decisions** that don't need protocol changes. The protocol already pushes enough state (`transcript`, `turn`, `pending_approvals`, `device`, etc.) for the client to make these decisions.

**Recommendation**: Invest heavily in the Go client's rendering layer. The server is largely correct; the gaps are in how the client presents the state.

### 8.2 Server-Side Enhancements

The server needs work on:
- Error recovery (retry logic, provider fallback, quota swap)
- Queue safety (guards, restore, clear on error)
- Conversation lifecycle (busy guard, model carryover, auto-title, bootstrap)
- Interrupt edge cases (backend cancel, approval cache, recovery alert)
- Auto-reflection triggers

These are **behavioral gaps** that require server-side logic.

**Recommendation**: Prioritize error recovery and queue safety — these affect reliability. Lifecycle polish can come later.

### 8.3 Protocol Evolution

The current protocol (`protocol.ts`) is sufficient for the core experience. Minor additions:
- `mod_context` completeness (for panel rendering)
- Trajectory stats (for statusline)
- `bash_mode` state (for bash UI)
- `transient_hint` frame (for statusline hints)

**Recommendation**: Don't expand the protocol until client-side rendering catches up. The protocol is not the bottleneck.

---

## 9. IMPLEMENTATION PRIORITY (Suggested)

### Phase 1: Critical Fixes (Daily workflow)
1. Paste placeholder system
2. Cursor navigation (Option+arrow, Home/End, forward delete)
3. Per-tool-type rendering (at least: bash, edit, write, search)
4. Inline approvals (or at minimum: approval stubs for sequential)
5. Static/live transcript split

### Phase 2: Reliability (Error handling)
6. Crash recovery / error retry
7. Queue safety nets
8. Interrupt edge cases
9. Conversation switching safety

### Phase 3: Polish (Feel)
10. Tool card polish (dots, colorized args, shell highlighting)
11. Animations (blinking dots, shimmer, token smoothing)
12. Statusline richness (transient hints, indicators)
13. Diff rendering (unified diff, context lines)
14. Welcome screen / help dialog
15. Custom deny input

### Phase 4: Power Features
16. Subagent display
17. Auto-reflection
18. @ file completion
19. Bash mode UI
20. Selector enhancements

---

*This document is a living analysis. As zc evolves, each gap should be checked off and linked to the implementing commit.*
