# Crush UI/UX Audit for ZeptoCodeCLI

Source: `/tmp/crush` (charmbracelet/crush @ 3446255, bubbletea v2 + ultraviolet + lipgloss/glamour v2 + charmtone).
Audience: zc (ZeptoCodeCLI) — thin Go/BubbleTea client over the letta-code app-server wire protocol.
License: crush is FSL-1.1-MIT; zc is private, so copying is permitted. Recommendation per item: **adopt pattern** (reimplement idea), **lift** (genuinely standalone code), or **skip**.

---

## Top-10 shortlist (value ÷ effort, zc-specific)

1. **Approval-dialog input grace period** (`dialog/dialog.go`) — absorb keystrokes for 200ms-quiet/1.5s-max when a dialog opens *asynchronously* (permission prompts), skip grace when the same dialog type reopens within 500ms. Kills "I was typing and accidentally approved a Bash call". Tiny effort, direct fit for zc's approval modal. **Adopt now.**
2. **Desktop notifications, focus-aware** (`internal/ui/notification/`) — native OS / OSC 99 (with capability query) / OSC 777 fallback / bell / noop, selected by config + SSH detection + focus-events support; only notify when terminal unfocused. Perfect for long agent turns. The OSC layer is nearly standalone. **Lift OSC+bell, adopt policy.**
3. **Streaming markdown v2** (`chat/streaming_markdown.go`) — same stable-prefix idea as zc's streammd.go but strictly better: (a) *incremental boundary promotion* — renders new safe chunks and glues them onto the cached prefix render, so the cached portion grows and the re-rendered tail stays tiny; (b) applies the same cache to the **thinking** stream separately; (c) three extra hazard classes zc lacks: loose lists (any list marker anywhere in prefix), HTML block openers, link reference definitions; (d) per-renderer mutex contract. **Adopt into streammd.go.**
4. **Live nested subagent tool rendering** (`chat/agent.go`, `Compactable`, `lipgloss/v2/tree`) — subagent (Task/Agent) cards show their nested tool calls live as a tree of compact one-line headers under the parent card, spinner at the bottom, result markdown when done. zc currently drops subagent stream_deltas entirely (rollups only). Wire has the data (subagent rollups/queue lines already parsed). Big legibility win for Jack's multi-agent workflows. **Adopt pattern.**
5. **Permission dialog power features** (`dialog/permissions.go`) — unified↔split diff toggle (`t`), auto-split when window ≥140 cols, fullscreen toggle (`f`), h-scroll (shift+arrows), direct hotkeys a/s/d (allow / allow-for-session / deny), dialog forced fullscreen under min window size. zc's approval modal has diff previews + numbered suggestions; the split/fullscreen/hotkey layer is the missing polish. **Adopt selectively.**
6. **List-cache architecture v2** (`list/list.go`, `list/item.go`) — items expose `Version() uint64` (bump-on-mutate) + `Finished() bool`; the list memoizes render keyed by (pointer, width, version) and *freezes* finished items (verbatim emit, no Render call). Plus `Prewarm(from,batch)` incremental cache warming, `Overflows(h)` bottom-walk instead of O(N) TotalHeight, and selection-drag freeze suppression. zc's list is the ancestor of this design; the version-counter + freeze + prewarm trio is the upgrade. **Adopt.**
7. **Resize settle + draw-level cache** (`model/chat.go`) — during resize: skip scrollbar + total-height scan, reflow only visible items; after a settle delay, `WarmStep` prewarms in batches across frames. Separately `chatDrawCache` caches the *ANSI-decoded ScreenBuffer* of the rendered list keyed by the rendered string, so byte-identical frames skip ANSI reparse entirely. Directly targets zc's known "resize under heavy streaming" risk. **Adopt.**
8. **Mouse text selection + copy** (`model/chat.go`, `list/highlight.go`, `chat/messages.go` `highlightableMessageItem`) — click-drag selection with line/col ranges, double-click=word, triple-click=line (400ms threshold), `c`/`y` copies selection (or focused message) via cross-platform clipboard pkg + OSC52-ish fallback, esc clears. Solves zc's shift+drag trade-off from mouse capture. **Adopt pattern (large but self-contained).**
9. **Paste intelligence** (`model/ui.go` `handlePasteMsg`) — \r\n normalization; paste exceeding lines/cols threshold auto-becomes a `paste_N.txt` attachment (5MB cap, MIME sniff); pasted file paths that all exist and are images become image attachments; ctrl+v reads an *image* from system clipboard. Attachment upload depends on letta wire support (check with letta audit — `create_message` attachments), but the long-paste-to-file trick works with plain text too (write temp file + @-mention). **Adopt threshold trick now, attachments when wire allows.**
10. **Session polish kit** (several) — per-turn `AssistantInfoItem` footer (`◇ model · via provider · 12.3s`), `maxTextWidth = 120` readability cap on prose (tools/diffs full width), thinking 3-state windowing (last-10 collapsed → last-200 tail-window → full, "Thought for Xs" footer, click/space to cycle), todo/queue **pills** above the input with ctrl+t expansion and auto-expand on tall terminals. **Adopt piecemeal.**

---

## 1. Chat / transcript rendering

### 1.1 Multi-layer render caching (the "F-series" design)
Crush's chat rendering is a stack of caches, each with explicit invalidation:

| Layer | Where | Key | Notes |
|---|---|---|---|
| Streaming glamour prefix | `chat/streaming_markdown.go` | width + literal byte-prefix | grows incrementally; separate instance for content and thinking |
| Per-section cache | `chat/assistant.go` `assistantSection` | width + FNV64(src) + FNV64(extras) | thinking/content/error each independent, so streaming content doesn't invalidate the (expensive) thinking render |
| Prefix-render cache | `chat/messages.go` `cachedMessageItem` | width + fingerprint(all section keys + composition + focus bit) | caches the per-line focus-prefix loop; bypassed while spinning/highlighted |
| List memo + freeze | `list/list.go` | (item ptr, width, version) | `Finished()` items frozen — no Render call at all |
| Draw cache | `model/chat.go` `chatDrawCache` | rendered string + width method | caches decoded `uv.ScreenBuffer`, skips ANSI reparse per frame |

Key idioms worth stealing wholesale:
- **`Versioned.Bump()`** on every observable mutation, including spinner ticks (`Animate` bumps so the memo doesn't serve a frozen spinner frame) — `chat/assistant.go:Animate`.
- **FNV-64 with length-prefix framing** (`fnvFields`) to avoid field-concatenation collisions in cache keys.
- **`compositionKey()`**: hash the *structural decisions* (finished flag, finish reason) separately from section content, so "Canceled" footers invalidate correctly.
- Render() applies focus styling by per-line string prefix, NOT lipgloss re-render — comment explicitly says lipgloss wrapping is too slow for long messages.

zc mapping: zc has per-entry render memo + stable-prefix streaming already; the deltas are (a) per-section split, (b) version counters instead of ad-hoc cacheKeys, (c) freeze, (d) draw-level buffer cache. Value: high under long transcripts/heavy streaming. Effort: medium (mechanical, well-documented in source).

### 1.2 Streaming markdown (see shortlist #3)
`assistant_section_cache_test.go`, `prefix_cache_test.go`, `incremental_glamour_test.go`, `resize_bench_test.go` are ready-made test blueprints — crush ships *benchmarks* for resize reflow. Also: glamour renderers are memoized **per width** (`common/markdown.go` `mdCache map[int]*TermRenderer`) with a "quiet" (colorless) variant for thinking text, invalidated on theme change; per-renderer mutexes because goldmark's BlockStack is stateful (zc solved this differently by pinning one renderer — the per-width memo is worth copying since width changes on resize).

### 1.3 Diffs
- `internal/ui/diffview/`: full unified **and split (side-by-side)** diff renderer with chroma syntax highlighting inside diff lines (`xchroma.Formatter` with per-line bg override), intraline word-level edits via `go-udiff`, line numbers, x/y offsets, `xxh3`-keyed internal caches, golden tests. **Genuinely liftable as a package** (depends only on chroma, udiff, x/ansi, lipgloss, its own style struct).
- `chat/unified_diff.go` routes Edit/MultiEdit tool cards through it; diffs are the only tool bodies that get *full* width (everything else capped at 120).

### 1.4 Errors, cancellation, misc message types
- Error section: `ERROR` tag + truncated title + details block, cached like other sections.
- `FinishReasonCanceled` renders a muted "Canceled" line appended to the message — cheap, legible turn-abort marker.
- `AssistantInfoItem` (`chat/messages.go`): after each finished turn, a one-line footer `◇ ModelName via Provider 8.2s`. With zc's per-conversation model switching this is genuinely useful history metadata (zc statusline shows only current model).
- User messages render attachments as chips via `attachments.Renderer` (image ■ / text ≡ / skill ▲ icons).

### 1.5 Width & resize behavior
- `cappedMessageWidth = min(width-2, 120)` for prose readability on wide terminals. Trivial, high-polish. zc renders full-width markdown today.
- Resize: `BeginResize()` sets a `resizing` flag (skip scrollbar/total-height, visible items only) + settle timer + `WarmStep` batched prewarm (see shortlist #7).

### 1.6 Anti-flicker / input flood
- `model/filter.go`: a `tea.WithFilter` input filter that **coalesces mouse wheel/motion to ~60Hz** before messages enter the update queue, accumulating wheel deltas (and cancelling on sign flip). This is upstream of zc's batch-drain-frames fix — dropping floods at the input boundary keeps keypresses from queueing behind mouse spam. Cheap to adopt.
- `KeyReleaseMsg` handling: crush doesn't need zc's swallow-hack because components are imperative (no generic input path) — zc's memory note stands.

## 2. Tool call presentation

### 2.1 Renderer registry
`chat/tools.go` `NewToolMessageItem` routes tool name → dedicated item type; ~14 renderer files (bash, file view/write/edit/download, glob/grep/ls/sourcegraph, fetch/websearch, agent, diagnostics, references, lsp_restart, todos, question, mcp_* prefix, docker MCP, generic fallback). Each implements `RenderTool(sty, width, opts)`. zc's toolcard.go already does per-tool headers; the interesting bits:
- **`Compactable`**: any tool can render as a single-line header; used for nested subagent tools. zc could reuse its ctrl+o collapsed mode.
- **`SpinningFunc`** override per tool (agent tool spins until result, not until args-finished).
- `toolHeader` truncates params to fit, but guarantees ≥30 cols for the main param (`minSpaceForMainParam`).
- `toolOutputCodeContent`: file-view output is chroma-highlighted with line numbers starting at the tool's offset param — tool result bodies look like editor excerpts, not raw text. (zc's known "chroma in tool bodies" idea — implementation reference is here + `common/highlight.go` + `xchroma`.)
- `toolOutputPlainContent`: collapsed = last `responseContextHeight`(10) lines; expanded = everything; body gets a left rail via per-line prefix (zc: same concept, 4 lines).
- **Hook indicator** (`toolOutputHookIndicator`): when hooks ran on a tool call, a compact line shows hook names + decisions. If zc surfaces letta hook events someday, copy this.

### 2.2 Nested subagent tree (shortlist #4)
`AgentToolMessageItem` holds `nestedTools []ToolMessageItem`; children are marked compact and rendered as `lipgloss/v2/tree` children of the parent header + Task-tag + prompt; parent bumps its version on child animation ticks (children aren't list items — parent embeds their render). `Chat.UpdateNestedToolIDs` maps child IDs → parent index so streaming updates find them. This is the design zc would need since the list is flat.

### 2.3 Status affordances
- Per-status icons/colors (pending/success/error/canceled) + status-tinted headers.
- `pendingTool()`: compact header with scramble-spinner while args stream.
- Space or click expands (`Expandable`), and `ctrl+d` toggles a global "details" mode (see 5.4 sidebar/compact).

## 3. Dialogs & overlays

### 3.1 Overlay stack (`dialog/dialog.go`)
- `Dialog` interface = `ID() / HandleMsg(msg) Action / Draw(scr, area) *tea.Cursor`. Actions are opaque `any` handled by the main model — dialogs never mutate app state directly.
- Stack with push/pop/`BringToFront`/`ContainsDialog`; only front dialog gets messages; all draw in order (true stacking, e.g. quit-confirm over sessions).
- **Grace period** (shortlist #1): `OpenDialogWithGrace` for async dialogs.
- `LoadingDialog` interface: front dialog can show spinner while its data loads.
- zc's dialog.go is single-modal; the stack + Action pattern + grace are all adoptable incrementally.

### 3.2 Command palette (`dialog/commands.go`)
- Radio tabs (tab/shift+tab): System / User(custom) / MCP prompts.
- Filterable list w/ match highlighting; items show shortcut in a right column that **auto-hides when >25% of row width** (`applyInfoColumnVisibility` — sessions dialog uses 35% for timestamps). Nice responsive-layout trick for zc's palette/pickers.
- Commands with arguments open a follow-up `arguments.go` form dialog (named args from frontmatter).
- Custom commands = markdown files w/ frontmatter — analogous to zc's mod/client commands.

### 3.3 Permissions (`dialog/permissions.go`) — see shortlist #5.
Also: option row is left/right-navigable buttons; enter confirms; `ctrl+y` doubles as confirm-select. `permission_test.go` + golden files cover it.

### 3.4 Sessions (`dialog/sessions.go`)
- In-dialog **rename** (`inline_editor.go` pattern: `question_editor` swaps into the row) and **delete with y/n confirm**, both guarded when the session is busy (agent running).
- Timestamps right-aligned per row, auto-hidden when narrow.
- zc's conversation picker could adopt rename/archive-in-place (wire: `conversation_update`, `/title` exists).

### 3.5 Models (`dialog/models.go` + `models_list.go`)
- Grouped by provider with section headers; radio for large/small model type; "recently used" hoisting; items show reasoning-capability badges; selecting an unconfigured provider drops into API-key entry (`api_key_input.go`) with **live validation spinner and error states**, then continues the original selection. zc's /model + /connect flows are separate — the seamless "pick model → realize no key → inline key entry → resume" chain is nice UX to emulate.

### 3.6 File picker (`dialog/filepicker.go`)
- Directory browser with **inline image preview** (kitty graphics or half-block fallback via `ui/image`) sized by real cell-size caps. Used for attachments. Relevant only if/when zc does attachments.

### 3.7 Question forms (`dialog/question_*.go` + `chat/question.go`)
- Full mini-framework: yesno/confirm/single/multi/freetext/editor/form, with a shared `question_choice_base.go` (hover, mouse, "Other…" free-text escape hatch). zc's form package covers most of this; notable extras: **inline editor mode** (question replaces the input area instead of a modal — `InlineEditor` interface in `dialog/inline_editor.go`, routed via `activeInline` in ui.go, suppresses pills, tab still toggles focus) and questions render into the transcript afterward as Q/A tool cards (`chat/question.go`). zc's AskUserQuestion could adopt the inline-at-editor presentation — far less jarring than a modal mid-stream.

## 4. Editor / input

- **Bang mode** (`!` on empty input): prompt icon flips to a ` ! ` badge (Turtle green), runs a local shell command, output lands in the transcript as `ShellItem` (left bar, last-10-lines collapsed, h-scroll via shift+arrows, exit code, pending spinner, ANSI16-remapped output), esc cancels the running command, backspace-on-empty exits bang mode, and results are persisted as message parts so they replay in history. zc equivalent would shell out locally (same box as app-server; server cwd known from `device_status`). Medium effort, very daily-drivable.
- **External editor**: ctrl+o opens `$EDITOR` (via `x/editor`) seeded with current input (bang prefix preserved), returns text on save. Cheap and loved. (zc note: zc uses ctrl+o for tool previews — pick another binding.)
- **History**: up/down when the caret is at first/last line (textarea-aware), persisted per project (`internal/history`). zc has input history already; the caret-position gating is the polish part.
- **Paste**: see shortlist #9. Threshold consts `pasteLinesThreshold/pasteColsThreshold`; `checkBangModeAfterPaste`.
- **Attachments strip** (`ui/attachments/`): chips above input; `ctrl+r` enters delete mode where each chip shows its index digit — press digit to delete one, `r` for all, esc cancels. Modal-free bulk edit; neat pattern.
- **Completions** (`ui/completions/`): popup anchored above input (zc now has this); extras worth copying: **tiered ranking** — exact name > name prefix > path segment > fzf fallback (`namePriorityRules`), **tab = insert-and-keep-open** vs enter = insert-and-close (`SelectionMsg.KeepOpen`), MCP resources mixed in, size clamps 10–100 cols / ≤10 rows, match-char highlighting in results.
- **Prompt-mode icons**: the textarea prompt itself signals mode — `>` normal, ` ! ` yolo (red badge, placeholder "Yolo mode!"), ` ! ` green for bang, ` ? ` for question mode. zc uses border color for permission mode; an icon in the prompt gutter is a good secondary signal (color-blind friendly).
- Newline: shift+enter *and* ctrl+j (zc uses ctrl+j too — matches).

## 5. Layout & chrome

- **Layout engine**: single `uiLayout` struct of rectangles computed in one place; `ultraviolet/layout.Vertical(...).Split(...).Assign(...)` splitters. Components draw into rects on a `uv.ScreenBuffer`; strings painted via `uv.NewStyledString(...).Draw(scr, rect)`. zc composes strings + compositor; fine as-is, but the *single layout struct* discipline is worth mirroring as zc chrome grows.
- **Header** (`model/header.go` + `ui/logo`): CRUSH wordmark from hand-built letterforms with per-grapheme gradient (`styles.ApplyBoldForegroundGrad`), diagonal `╱` field filler, random letterform stretch (cached to avoid jitter), compact variant below breakpoint 120 cols, version string. Pure whimsy but it's a large part of crush's perceived polish. `logo/` is standalone-liftable if zc ever wants a ZEPTO wordmark.
- **Sidebar** (`model/sidebar.go`): wide mode shows logo, cwd, model info (name, provider, reasoning setting, **context% + cost**, credits), then Modified Files / LSPs / MCPs / Skills sections with a **fair dynamic height allocator** (`getDynamicHeightLimits`: min 2 rows each, round-robin the surplus by need) and "+N more" truncation hints. zc deliberately keeps a bottom statusline instead of a sidebar; if that ever changes, this is the reference. The allocator itself is a reusable algorithm for any multi-section panel (e.g. zc's /jobs panel).
- **Compact mode + details overlay**: below breakpoint the sidebar collapses into the header; `ctrl+d` opens a session-details overlay (same info), auto-closes when typing starts.
- **Status bar** (`model/status.go`): bubbles/help-driven contextual key hints (change with focus/state) + transient info messages with TTL (default 5s; error/warn/info styling with colored indicator). zc's statusline hint string is hardcoded — the contextual-keymap-help pattern fixes that permanently, and `util.InfoMsg`+TTL gives zc non-blocking toasts (e.g. "copied", "reconnected").
- **Pills** (`model/pills.go`): see shortlist #10. Detail: queue pill renders count as gradient `▶▶▶` triangles; todo pill embeds live spinner + activeForm of the in-progress todo; expanded panel is focusable (left/right switches todo/queue section).
- **Landing view** (`model/landing.go`): pre-session screen = cwd + model info + three status columns (LSP/MCP/Skills). zc's startup pickers could land on something similar (agent info + memory/skills/mods status) instead of jumping straight to chat.
- **Onboarding** (`model/onboarding.go`, `dialog/oauth*.go`): provider choice → OAuth device-code flows (Copilot/Hyper) or API key → model pick, all as dialogs over the landing screen. zc scope-out (native `letta` handles login) — skip.

## 6. Theming & styles

- `styles/styles.go` is one giant semantic `Styles` struct (nested groups per component). Built by `quickstyle.go` from ~30 semantic slots: primary/secondary/accent/keyword, 4 fg tiers, 4 bg tiers, status colors (error/warn/info/success ± subtle variants), and a full **ANSI-16 palette mapping**. Themes = one function each (`CharmtonePantera` default, `HypercrushObsidiana`), selected per provider with a cheap `ThemeKeyForProvider` identity check to skip rebuilds. zc's palette.go is close in spirit; the quickStyle "semantic slots → whole style tree" builder is the scalable version.
- **`common/ansi16.go RemapANSI16`**: rewrites basic SGR 30–37/90–97 (and 256-color 0–15) in captured program output to explicit truecolor from the theme, so `ls --color`/git output stays legible on the theme background. Standalone-liftable; useful for zc's Bash tool bodies and jobs logs.
- **Gradients** (`styles/grad.go`): per-grapheme `lipgloss.Blend1D` ramps (`ForegroundGrad`/`ApplyBoldForegroundGrad`) — used for logo, dialog titles (`DialogTitle` gradient rule line), queue triangles, spinner. Charmtone provides the anchor colors.
- **Dark/light**: crush is dark-theme-only by default (no LightDark juggling) — zc already resolves light/dark once at startup; keep.
- Icons are centralized consts (`✓ ⋯ ⟳ ◇ ◆ → ■ ≡ ▲`); tool status icons unified. zc: same idea exists, keep consolidated.

## 7. Animation

`ui/anim/anim.go` — the known "color-cycling spinner", details now confirmed:
- 20 FPS step-driven (deterministic, testable — no wall clock in render), staggered per-char "birth" (~1s entrance), scrambled rune cycling with gradient ramp across chars, optional label + animated ellipsis (400ms period) + **dynamic suffix func** (crush passes `common.Elapsed()` — global turn timer `timer.go` StartTurn/StopTurn, so every spinner shows live elapsed time).
- Prerendered frame cache when colors don't cycle; settings-hash cache (`xxh3`) shares frame tables across instances; `NoScramble` mode for non-LLM contexts.
- IDs route `StepMsg` to the right spinner; visible-only animation: `Chat.Animate` only ticks items in view, `RestartPausedVisibleAnimations` on scroll.
- **Liftable nearly as-is** (deps: csync, xxh3, colorful). The "elapsed time suffix on the working spinner" is the single best piece of feedback polish for zc turns.

## 8. Terminal integration extras

- **Capabilities detection** (`common/capabilities.go`): queries terminal for kitty graphics, sixel, cell size (used to size image previews), OSC99 support (`notification/osc.go` sends an OSC99 query with `p=?` and parses the reply), focus-event support; gated by env heuristics (`shouldQueryCapabilities`). zc already probes background color pre-tea; generalizing into a Capabilities struct is the clean path if zc adds images/notifications.
- **Images** (`ui/image`): kitty graphics transmit-once-then-place (TransmittedMsg), `imaging` resize to cell grid, half-block ANSI fallback via go-ansi-paintbrush, FNV-keyed cache + `ResetCache`. Only relevant with attachments/wire support.
- **Clipboard** (`internal/clipboard`): build-tag-guarded golang-design/clipboard wrapper (text + PNG read/write), no CGO on unsupported platforms. Liftable; pairs with selection-copy and image paste.
- Terminal window title: zc already sets it (tea.View.WindowTitle); crush same.

## 9. Performance rules (their AGENTS.md, distilled for zc)

- Single top-level model; children are imperative structs with `Update`-less APIs returning "consumed" bools — no Elm message fan-out. (zc is mostly there; codify it in zc's own AGENTS.md.)
- Never IO in Update; all IO via tea.Cmd; never mutate state inside a Cmd.
- All ANSI string ops via `x/ansi` (Cut/StringWidth/Strip/Truncate) — never byte-level.
- List renders visible items only; caching is the *item's* job; list-level memo keys on (ptr,width,version).
- `tea.Batch` for multi-cmd; account for borders/padding in every width calc.
- Golden-file tests (`catwalk`) for every component + benchmarks for hot paths (resize). zc's tmux gauntlet covers E2E; component goldens would slot in under `pixi run test`.

## 10. Odds & ends worth noting

- **Yolo mode**: ctrl+y toggles permission-skip; the *editor prompt* becomes a red ` ! ` badge + "Yolo mode!" placeholder — mode is unmissable. zc's border-color-per-mode is subtler; consider the badge for `unrestricted`.
- **Scrollbar** (`common/scrollbar.go` + modes): config `always/default/never`; in default mode it appears on scroll and auto-hides after a TTL (sequence-numbered hide cmd to avoid races). One-column reserve only when shown.
- **Todos tool card** (`chat/todos.go`): renders `N/M · current task`, with richer "just started/completed X" phrasing from result metadata; pills mirror it globally. If letta's TodoWrite events reach zc (check letta audit), this is the render reference.
- **MCP/LSP status rows** (`model/mcp.go`, `model/lsp.go`): online/busy/error/disabled icons with per-state colors + error truncation. Generic "service status row" pattern — reusable for zc's broker/jobs panel.
- **Session busy guard**: sessions dialog refuses delete/rename of a session whose agent is mid-turn — zc's /tidy and pickers should adopt the same guard using runtime state.
- **`stringext`/`ansiext`/`csync`** small util packages — `csync.Map/Slice` (typed concurrent containers) and ansiext are liftable helpers.
- Crush persists **prompt history per project** and reconstructs bang-shell items from message parts on replay — replay fidelity for non-agent items; zc's /search-style local scans could do the same for its own client-side items if any are added.

## Explicit skips (and why)

- **Ultraviolet screen-buffer hybrid rendering**: zc's compositor approach works; migrating to uv.ScreenBuffer rects is a big rewrite with modest payoff — take the *draw cache* idea without the framework.
- **SQLite persistence / sessions model / LSP / MCP internals**: server-side concerns in zc's architecture; letta app-server owns them.
- **OAuth/onboarding dialogs**: zc scope-out (native CLI owns login).
- **Hyper credits / provider theming switch**: crush-business-specific; the *mechanism* (theme key check → rebuild styles → `ClearItemCaches` + `InvalidateMarkdownRendererCache`) is the part to remember if zc ever gets theme switching.
- **bubblezone**: not used by crush at all — they do manual hit-testing from layout rects (findItemAtY + per-item HandleMouseClick with local coords). One less dep for zc's mouse plans.

## Suggested adoption order for zc

1. Grace period on approval modal (hours).
2. Spinner elapsed-time suffix + turn timer; InfoMsg toasts with TTL in statusline (hours).
3. Streaming-markdown upgrades: thinking-stream cache, incremental promotion, extra hazards + port their test matrix (1–2 days).
4. Focus-aware notifications: bell + OSC99/777 first (works over SSH), native later (1 day).
5. List v2: Versioned/Finished freeze + Prewarm + resize settle + draw cache (2–3 days, benchmark with their resize_bench pattern).
6. Permission dialog: a/s/d hotkeys, fullscreen, unified/split toggle reusing diffview (1–2 days; consider lifting `diffview` wholesale).
7. Nested subagent tool tree from rollup frames (2–3 days incl. wire mapping).
8. Mouse selection + clipboard copy (3+ days; biggest UX unlock for the mouse-capture trade-off).
9. Paste intelligence + external-editor binding (1 day; attachments pending wire check).
10. Pills for todos/queue + thinking tail-window + AssistantInfoItem + 120-col cap (polish batch).
