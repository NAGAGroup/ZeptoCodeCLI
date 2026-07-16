# Crush vs zc: implementation-quality audit

Comparative pass (follow-up to `audit-crush-ux.md`, which was gap-oriented): features **both** clients implement, where crush's implementation is better, judged source-to-source.

- zc source: `cmd/zc/*.go`, `internal/ui/*` @ 5b38a42
- crush source: `/tmp/crush` (charmbracelet/crush @ 3446255)
- Recommendation legend: **adopt** = reimplement the pattern · **lift** = copy nearly as-is (FSL-1.1-MIT permits; zc is private) · **rewrite** = zc's approach is structurally wrong, replace

---

## Top-10 shortlist (weighted by Jack's pain: input + clutter first)

1. **Kill `autosizeInput`, turn on `DynamicHeight`** — zc counts hard `\n`s only (`main.go:710`), so soft-wrapped long lines never grow the box and pasted text leaves the height stale. bubbles v2 textarea already solves this (`DynamicHeight=true` + `MinHeight`/`MaxHeight`, crush `ui.go:333`). Removes a whole class of "annoying input" at negative cost. *Effort: hours.*
2. **Port crush's history state machine** (`model/history.go`, ~180 lines) — zc's up/down recall (`main.go:928`) destroys drafts, hijacks arrows inside recalled multi-line text, forgets edits, and evaporates on restart. Crush: draft preservation, edits exit history mode, cursor-position-gated navigation, esc restores draft, persisted history. *Effort: 1 day.*
3. **Fix the esc priority chain** — zc's esc with the completion dropdown open **wipes the entire input** (`main.go:851` never checks `completion.visible`). Correct order: close completions → exit history-nav (restore draft) → clear input → abort turn. *Effort: hours.*
4. **Stop stealing editor keys; add a focus model** — zc globally grabs ctrl+u/ctrl+d/pgup/pgdn for transcript scroll (`main.go:963`), shadowing the textarea's readline bindings (ctrl+u = delete-before-cursor in bubbles v2) *while the user is typing*. Crush routes keys by focus (tab toggles editor↔chat; vim scroll keys only in chat focus). Minimum fix: gate scroll keys on empty input; right fix: focus toggle. *Effort: 0.5–2 days.*
5. **Transcript grammar rewrite (de-clutter spec below, §3.3)** — drop the `you ▸` / `● agent` label lines, use per-line role gutters like crush (user = colored left border every line; assistant = plain 2-col inset), glamour-render user text, cap prose at 120, add a dim per-turn footer. *Effort: 1–2 days, mostly renderEntry.*
6. **Route transient feedback to statusline toasts, not transcript entries** — zc appends `entryInfo` for mode/model/title/answered/reconnect/abort events; crush shows them as TTL'd status messages (`model/status.go`, 5s) and keeps the transcript for content. Single biggest "cluttered" fix after #5. *Effort: 1 day.*
7. **Show diffs in Edit tool cards, stop pre-truncating tool returns** — zc renders a diff only in the approval modal and throws it away; completed edits show 4 dim lines of tool-return text. Store `req.Request.Diffs` on the tool entry and render the colorized diff in the card. Separately, zc truncates tool output **at ingest** (`main.go:1809`: 31 lines/6000 bytes) so expansion can never show more — store full, truncate at render like crush. *Effort: 1 day; diffview lift later.*
8. **Replace byte-slice truncation with `x/ansi`** — `compactOneLine`/`truncateLines` (`main.go:2303`) cut by bytes: they split UTF-8 runes and, in `overlay.render` (`overlay.go:113`), truncate **already-styled** strings mid-ANSI-sequence → color bleed across the screen. Crush rule: `ansi.Truncate`/`ansi.StringWidth` everywhere. Live corruption bug. *Effort: hours.*
9. **Approval modal: input grace period + parity keys** — zc's single-key a/d/y/n/1-9 modal acts on in-flight keystrokes the instant it pops (crush absorbs input 200ms-quiet/1.5s-max, `dialog/dialog.go:OpenDialogWithGrace`); crush also offers allow-for-session and diff scroll/fullscreen. *Effort: grace = hours; rest = 1–2 days.*
10. **Never do IO in Update** — `/jobs` calls the broker over HTTP with a 2s timeout **synchronously on the UI thread** (`overlay.go:184` via `clientCommands` run). Crush's first rule in `internal/ui/AGENTS.md`: IO only in `tea.Cmd`. Same for `loadJobs()` disk scans. *Effort: hours.*

---

## 1. Input text handling

zc's editor is a bare `bubbles/v2 textarea` plus ad-hoc key handling in `handleKey` (`main.go:726-993`). Crush uses the same textarea but configures it properly and wraps it with a real interaction layer. Concern-by-concern:

### 1.1 Dynamic height / wrapping
- **zc** `main.go:710-724`: `lines := strings.Count(Value(), "\n")+1; h = clamp(lines+1, 3, 8)`. Soft-wrapped lines don't count → a 300-char single-line prompt renders inside a 3-row box with hidden text. The `+1` slack row is a fudge. `autosizeInput` is only called on the key path, so **paste and programmatic SetValue leave the height stale** (§1.4).
- **crush** `ui.go:333-341`: `ta.DynamicHeight = true; ta.MinHeight = TextareaMinHeight; ta.MaxHeight = TextareaMaxHeight; ta.CharLimit = -1; ta.SetVirtualCursor(false)`. The textarea computes wrapped display height itself; the model only reacts to height *changes* (`handleTextareaHeightChange(prevHeight)`) to re-layout and keep the chat anchored. Every programmatic mutation is followed by `updateTextareaWithPrevHeight(nil, prevHeight)` so the internal view/cursor stays consistent.
- **Recommendation: rewrite** — delete `autosizeInput`, enable DynamicHeight, react to height deltas. Also steal `updateTextareaWithPrevHeight` as the idiom for every `SetValue`/`Reset`/`InsertString` call site (zc has ~8).

### 1.2 History navigation
- **zc** `main.go:928-962`: recall requires input empty or already navigating. Problems, all confirmed in source:
  - No draft: typing something, clearing it, pressing up, then down past the end → `Reset()` — original draft unrecoverable.
  - Once `historyIdx < len(history)`, **any** up/down replaces the buffer — you cannot cursor-move inside a recalled multi-line message; edits to a recalled message are clobbered by the next arrow press (editing doesn't reset `historyIdx`).
  - `history` is in-memory only, lost on restart; slash commands are recorded but `SetValue` recalls them without re-triggering completion state properly (calls `refreshCompletions`, ok).
- **crush** `model/history.go`: `promptHistory{messages, index, draft}`. Entering history saves the draft; `updateHistoryDraft` resets `index=-1` on any text modification (edits exit history mode cleanly); up only recalls when the cursor is at (0,0) (`isAtEditorStart`), else it first moves the cursor to line start, else normal cursor movement — same on down with `isAtEditorEnd`; esc restores the draft; history is loaded from persistence per session (and includes `!` shell commands, re-prefixed); reloaded after each send.
- **Recommendation: adopt** the whole state machine; persist zc's history to `~/.letta/zc-history.jsonl` (or reuse `/search`'s local-backend scan for seed data). The cursor-gated arrows alone remove the most annoying multi-line editing conflict.

### 1.3 Esc semantics
- **zc** `main.go:851-864`: esc = clear whole input if non-empty, else abort turn. Two destructive actions on one unmodified key, and the completion dropdown is not considered — **esc while the dropdown is open erases your typed command**. No recovery path (no draft, no undo).
- **crush**: esc is contextual — close completions, exit history (restore draft), clear text selection, cancel-delete-mode for attachments; turn-cancel is a separate explicit binding in chat focus. Nothing destroys typed text.
- **Recommendation: rewrite** the esc chain: `completion.visible → hide dropdown` → `historyIdx < len → restore draft` → `input non-empty → clear (and stash to a one-slot kill buffer)` → `turn active → abort`.

### 1.4 Paste
- **zc**: no `tea.PasteMsg` case; paste falls through `Update`'s tail into `m.input.Update(msg)` (`main.go:624-629`) — so after a paste: **no autosize** (box stays 3 rows), **no completion refresh**, **no CRLF normalization** (Windows-origin clipboard leaves `\r` in the buffer, which later renders as artifacts and is sent to the agent).
- **crush** `ui.go:4244-4315`: normalizes `\r\n`, routes to dialogs when open, converts oversized pastes (lines/cols thresholds, 5MB cap, MIME sniff) into attachments, detects pasted file paths, and goes through the prev-height update path.
- **Recommendation: adopt** a `tea.PasteMsg` case: normalize, insert, autosize (moot after #1.1), refresh completions. The paste-to-attachment tier can wait for attachment wire support (flagged in the letta audit).

### 1.5 Completions interaction
- **zc** `completion.go`:
  - `refreshCompletions` bails on any `\n` in the value (`completion.go:53`) — **@-completion is dead in multi-line input** (i.e. immediately after ctrl+j).
  - Fragment = last whitespace token of the whole buffer, not cursor-relative; `suffix` is always `""` and accept does `SetValue(prefix+insert)` + `CursorEnd` (`completion.go:124-132`) — completing with the cursor mid-buffer **silently deletes everything after the fragment** and teleports the cursor.
  - Enter accepts only the `/command` case (`main.go:900-906`); with an @-dropdown open, enter **sends the message** with the fragment unresolved. Tab/→ only.
  - Flat fuzzy subsequence score; no exact/prefix/path-segment tiering, no match highlighting; dir listing is case-insensitive-prefix only.
- **crush** `completions/` + `ui.go:2352-2420`: `@` opens only at buffer start or after whitespace and records `completionsStartIndex`; filtering is anchored to that index; closes on space or when the cursor moves before the anchor; enter = insert+close, tab = insert+keep-open (`SelectionMsg.KeepOpen`); tiered ranking (exact name > name prefix > path segment > fuzzy, `namePriorityRules`); match-char highlighting; x/ansi-safe sizing (10–100 cols, ≤10 rows).
- **Recommendation: adopt** anchor-index tracking + enter-accepts + tier ranking. The anchor also fixes the multi-line case for free (no more whole-buffer scan).

### 1.6 Unicode / ANSI width discipline
- **zc**: `compactOneLine`/`truncateLines`/`shortID` slice by **bytes** (`main.go:2303-2320`). Three concrete failure modes: (a) mid-rune cuts on CJK/emoji → mojibake `…`; (b) width measured as `len()` → CJK overflows its column; (c) `overlay.render` (`overlay.go:100-113`) applies `styleOverlaySel.Render` **before** `compactOneLine`, so truncation can land inside an SGR escape sequence → unterminated color state bleeding into subsequent lines. `toolParams` header budget also uses `len(name)` (`toolcard.go:122`).
- **crush**: hard rule (their `AGENTS.md`): all ANSI-adjacent string ops via `x/ansi` (`Truncate/StringWidth/Cut/Strip`); every renderer follows it.
- **Recommendation: rewrite** the three helpers on `ansi.StringWidth`/`ansi.Truncate` and grep every `[:n]`/`len(` in render paths. Hours of work, removes a live corruption bug.

### 1.7 Message routing (the KeyReleaseMsg hack)
- **zc** `main.go:415-419` swallows `tea.KeyReleaseMsg` globally (papering over releases reaching the generic input path), and the `Update` tail forwards **all** unhandled messages to the textarea (`main.go:624-629`).
- **crush**: components are imperative (no `Update(tea.Msg)` fan-out); the model explicitly dispatches `KeyPressMsg` and forwards only what it chooses. Releases never reach anything by construction.
- **Recommendation: adopt** allow-listing on the tail: forward only cursor-blink/textarea-internal message types + `KeyPressMsg`/`PasteMsg`. Keep the release swallow as belt-and-braces, but it stops being load-bearing.

### 1.8 Key conflicts (see shortlist #4)
zc intercepts ctrl+u/ctrl+d (textarea: delete-before-cursor / delete-char-forward) and pgup/pgdn globally for scrolling; overlay filter input is append/backspace-only (no cursor, no paste — crush uses a real `textinput` in every dialog). Adopt focus routing; use `textinput` for overlay filters.

### 1.9 What zc gets right (keep)
Batch-draining frame channel; content-change-gated `refreshCompletions` (the blink-tick fix); one-shot pre-tea background detection; swallowing raw tab; optimistic mode cycling. None of these regress under the changes above.

---

## 2. Tool output visualization

zc `toolcard.go` (183 lines, one generic renderer + per-family param extraction) vs crush `chat/*.go` (14 per-tool renderers over shared helpers).

### 2.1 Edit diffs — the biggest quality gap
- **zc**: `renderDiffs` (`main.go:2031-2059`) exists **only in the approval modal**: flat `+`/`-` colored lines, 16-line cap, no line numbers, no intraline, no syntax color, single column, `(no preview: reason)` fallback. After approval, the DiffPreview is discarded; the transcript card for a completed Edit shows the generic 4-line dim tool-return text — **the transcript never contains a diff**. The pager's `diffColorize` (`pager.go:82`) is the same flat scheme for memory-repo diffs.
- **crush**: Edit/MultiEdit cards render a real diff (`chat/file.go` → `toolOutputDiffContent` → `diffview/`): unified or side-by-side (auto-split ≥140 cols in the permission dialog), chroma syntax highlighting *inside* diff lines with per-line backgrounds, **intraline word-level** highlights via `go-udiff`, line numbers, hunk separators, per-file headers, x-scroll; diffs are the one tool body that gets **full width** (`hasCappedWidth=false`, `tools.go:170`) while prose is capped at 120; MultiEdit appends a "N of M edits failed" note; a denied edit shows `ERROR/WARN` tag *above* the diff so you still see what would have changed.
- **Recommendation** (staged): (1) *adopt now* — keep `req.Request.Diffs` on the tool entry at approval time and render it (current flat colorizer) as the Edit card body; (2) *lift* `internal/ui/diffview` (deps: chroma, go-udiff, x/ansi, lipgloss — self-contained) and route both the approval modal and cards through it; (3) diff metadata may also arrive in letta's `tool_return` — check with the Ecosystem Expert whether Edit returns structured old/new like crush's metadata, which would cover edits that never hit an approval.

### 2.2 Truncation strategy (structural difference)
- **zc** truncates **at ingest**: `e.toolReturn = truncateLines(ret, toolBodyExpandedLines+1, 6000)` (`main.go:1809`) — data beyond 31 lines/6000 bytes is *gone*; ctrl+o "expand" can never show it, and the byte cap can cut mid-rune. Expansion is a **global** toggle (`m.showToolOutput`) flipping every card at once, 4 ↔ 30 lines.
- **crush** stores full results and truncates **at render**: collapsed = last/first 10 lines + `… (N lines hidden) [click or space to expand]`; expansion is **per-item** (space/click on the focused card) and shows *everything*; headers wrap instead of truncate when expanded.
- **Recommendation: rewrite** storage (keep full text on the entry; render-side capping) and consider per-entry expansion once the transcript has item focus (see §4.1). Global ctrl+o can stay as "expand all" on top.

### 2.3 Body presentation per tool kind
| Kind | zc (`toolcard.go`, `renderToolBody`) | crush | Verdict |
|---|---|---|---|
| Bash output | raw text behind `  │ ` rail, dim, per-line `compactOneLine` (byte truncation); raw ANSI in output **bleeds** through `styleToolBody.Render` | `RemapANSI16` onto theme palette + `NormalizeSpace`, `ansi.Truncate`, width-styled content lines (`tools.go:644`) | crush strictly better; lift `common/ansi16.go` |
| Read/View | same generic dim rail | syntax-highlighted code, line numbers starting at request offset, bg code block, image results as `Loaded Image → mime size` line (`tools.go:677,731`) | crush strictly better; needs chroma + offset from args (zc has both in hand) |
| Grep/Glob | pattern+path header (good), generic body | same header idea but kv params (`include=`, `literal=`), empty-result → header only (no empty rail block) | minor: adopt kv params + empty-result suppression |
| Web | query in header, generic body | fetch/websearch renderers with URL params, markdown bodies | minor |
| Error state | status word in header suffix; error text hidden in the (collapsed) body | `ERROR`/`WARN` tag + first line of the error inline, truncated with ellipsis; "denied" gets WARN not ERROR (`tools.go:545`) | adopt — error visibility without expanding |
| Pending | static `●` icon | per-card scramble spinner + elapsed | already in gap-audit backlog |

### 2.4 Header/params micro-techniques worth copying (`tools.go:580-643`)
- **`minSpaceForMainParam = 30`**: kv extras (`limit=100, offset=40`) are appended *only if* the main param still keeps ≥30 cols — headers never squeeze the filename to make room for trivia. zc's single-string `toolParams` + byte-budget `max(20, w-len(name)-8)` has no such guarantee.
- Params as `[]string{main, k1, v1, k2, v2}` — uniform scheme across all renderers instead of zc's per-family string concat.
- Expanded mode **hard-wraps** the params with continuation-line indent aligned under the param column instead of truncating.
- `fsext.PrettyPath` (~ home shortening) everywhere zc uses raw `filepath.Rel`.

---

## 3. Chat window formatting / presentation

### 3.1 zc's clutter sources (as rendered by `renderEntry`, `main.go:1958-2029`)
1. **Role label noise**: every user line starts `▌ you ▸ `, every assistant message gets a `● agent` header line. In a long exchange that's 2 extra visual tokens per message that carry no information after the first.
2. **Broken user gutter**: `wrap.Render(rail + "you ▸ " + text)` puts the colored bar on the *first wrapped line only* — continuation lines are flush-left, so multi-line user messages visually dissolve into the background rhythm.
3. **No left inset for assistant/markdown**: assistant prose renders at column 0, full terminal width — headers, lists, code blocks all start at the same x as tool cards, info lines, and user rails. Nothing separates "voice" from "machinery".
4. **Reasoning placeholder per burst**: `· reasoning (N chars — ctrl+r expands)` appears for *every* reasoning segment (`entryReasoning` is opened per contiguous burst) — several per turn on reasoning-heavy models.
5. **Transient events as transcript entries**: `answered: …`, `switched to …`, `model → …`, `title set`, `⏹ abort requested`, `reconnected`, mode-change failures — all permanent `entryInfo` rows.
6. **Repeated affordance hints**: every truncated tool body ends `… +N lines (ctrl+o expands)`; every entry a hint that belongs in help.
7. **Uncapped prose width**: on a wide terminal, paragraphs run 200+ cols (crush caps at 120 — `maxTextWidth`, `chat/messages.go:29`).
8. **Uniform gap=1 with no turn grouping**: user→assistant→6 tool cards→assistant all have identical spacing; no visual "turn ended" marker.

### 3.2 Crush's transcript grammar (for contrast)
- **Zero role labels.** Identity is carried by the left gutter: user = `▎`-style left **border on every line** (primary/purple) + 1-col padding, body **markdown-rendered** even for user text (`quickstyle.go:856-862`, `chat/user.go`); assistant = plain 2-col inset (no bar when blurred; subtle green bar only when focused). Tools = status-icon-led cards at the same 2-col rhythm.
- Reasoning lives **inside** the assistant item as a bordered ThinkingBox (last-10-lines collapsed, "Thought for 12s" footer) — one block per turn, not per burst.
- Per-turn terminator: `AssistantInfoItem` — a dim `◇ model via provider · 8.2s` section footer after each completed assistant turn.
- Prose capped at `min(width-2, 120)`; tool bodies inset 2; **diffs full width** (the one deliberate exception).
- Ephemeral feedback → status bar with TTL, never the transcript.

### 3.3 Proposed transcript style spec for zc (concrete, derived)
```
gutter column: 2 cells for every entry kind (uniform left rhythm)

user:        "▌ " colored theme.User on EVERY line (apply per-line, like
             crush's BorderLeft, not string-prefix-then-wrap);
             body = glamour-rendered at min(w-2, 120)
assistant:   2-space inset, NO label line; body = glamour at min(w-4, 120)
reasoning:   folded into the turn: one line "· thought (ctrl+r)" collapsed,
             or italic dim block behind the same 2-space inset expanded;
             merge consecutive reasoning bursts into one entry
tool card:   icon + Name + dim params (unchanged), body inset "  " + dim;
             truncation line: "… +N lines" (drop the per-card key hint)
turn footer: dim "◇ sonnet-4-5 · 12s" after each assistant turn
             (zc already has lastUsage + model handle; add turn timer)
info/error:  errors stay in transcript (red, 2-space inset);
             transient info → statusline toast (TTL 5s, InfoMsg pattern)
spacing:     gap=1 between entries (keep); no extra blank inside entries;
             tool-call runs between two assistant texts get gap=0 within
             the run (group the machinery visually)   [optional refinement]
width:       every prose renderer capped at 120
```
Effort: this is almost entirely `renderEntry` + `theme.go` + the toast plumbing from crush's `util.InfoMsg`/`model/status.go` — 1–2 days, no architecture change, and it addresses "cluttered / weak user-agent distinction" head-on.

### 3.4 Streaming markdown quality delta
zc's `streamMD` (`streammd.go`) is a valid but blunter version of crush's:
- Boundary check scans only the **last 200 chars** for `|`/`===`/`---` (`streammd.go:43-49`) — a table or setext earlier in the prefix is missed (crush checks whole-prefix hazards incl. loose lists, HTML blocks, link-ref definitions), and `---` also matches a thematic break/frontmatter → over-conservative fallbacks (perf, not correctness).
- On every frame with an advanceable boundary, zc **re-renders the entire prefix** (`streammd.go:98-103`) — O(prefix) per advance; crush renders only the *new chunk* and glues (`streaming_markdown.go` boundary promotion), O(delta).
- zc has no streaming cache for reasoning text (currently plain-rendered, so moot — becomes relevant with §3.3's reasoning block).
- Crush ships a test matrix (`incremental_glamour_test.go`, `prefix_cache_test.go`, boundary goldens); zc's streammd has zero tests.
- **Recommendation: adopt** incremental promotion + whole-prefix hazard scan + port their table-driven boundary tests.

---

## 4. General UX patterns, latent bugs, papered-over workarounds

### 4.1 Focus & selection
zc has no transcript focus: no way to select a message, copy one, or expand one item. Crush's tab-focus + selectable items is the enabler for per-item expansion (§2.2), c/y copy, and keeps global keys from fighting the editor (§1.8). Biggest single structural UX adoption available. *Effort: medium (list selection + focus routing).* 

### 4.2 Latent bugs found in zc source (beyond input section)
- **UI-thread IO**: `/jobs` → `jobsOverlayItems()` → `brokerStatusLine()` does a synchronous HTTP GET with 2s timeout inside command dispatch (`overlay.go:173-201`); `loadJobs()` walks `~/.letta/jobs` on the same path. UI freezes up to 2s if the broker is wedged. → `tea.Cmd`, like every other picker already does.
- **ANSI-in-tool-output bleed**: `renderToolBody` styles raw return text; a tool that emits color (git, ls) leaves unbalanced SGR state inside the dim rail. → `ansi.Strip` or lift `RemapANSI16`.
- **Overlay style-then-truncate** corruption (§1.6c).
- **Length-based cache keys**: `entryCacheKey` uses `len(e.text)` (`main.go:1950`) — correct only because entries are append-only today; any future in-place mutation of equal length serves a stale render. Crush hashes content (FNV-64). Cheap hardening when touched next.
- **Approval ctrl+c inconsistency**: inside the approval modal ctrl+c quits instantly with no double-press arming (`main.go:814-817`), unlike the main path (`main.go:841-850`). Mid-turn + modal is exactly when an accidental quit hurts most.
- **`compactOneLine` on conversation titles with wide chars** in the statusline (`main.go:2134`) — same byte-truncation family.
- **Toggling ctrl+r/ctrl+o while scrolled up**: item heights change under the viewport; `offsetLine` can exceed the new item height until the next `normalize()` — a one-frame jump. Crush bumps versions and re-anchors (`SetSize` AtBottom check). Minor.

### 4.3 Micro-interaction deltas (same feature, crush feels better)
| Interaction | zc | crush | Worth it? |
|---|---|---|---|
| Send with trailing `\` | sends the backslash | strips it and inserts newline (`ui.go:2245`) — muscle-memory-friendly newline | yes, trivial |
| Typing `exit`/`quit` | sends to agent | opens quit dialog | yes, trivial |
| Statusline hints | hardcoded string `"^k cmds · ^p convs …"` (`main.go:2139`) — zc *has* `keymap.go` + `help.Model` but doesn't use ShortHelp here (memory notes it must be updated manually) | contextual `help.View(keyMap)` — hints change with focus/state | yes — delete the hardcoded string, render `helpModel.ShortHelpView(keys.ShortHelp())` |
| Busy guards | none — switching agent/conversation mid-turn is allowed and orphans the stream | `isAgentBusy()` warnings before new-session/editor-open (`ui.go:2287,2306`) | yes — zc knows `turnActive()`; gate destructive switches with a toast |
| Empty-result tool cards | rail block renders even for empty output ("" check exists, ok) — but "No output" style content shows raw | `HasEmptyResult()` → header only; bash "no output" sentinel suppressed | minor |
| Spinner idle stop | zc stops ticking when idle (good) | visible-only animation + restart on scroll | parity-ish |
| Info message lifetime | permanent transcript rows | TTL toasts + `ClearInfoMsg` | covered in #6 |
| Placeholder | static | randomized, mode-reactive ("Yolo mode!", "Run a shell command") | cosmetic, cheap |
| Startup | in-TUI phase machine, centered spinner/pickers (good; recently fixed resume bug) | landing page with cwd/model/LSP/skills columns | zc's is fine; landing page is a later nicety |

### 4.4 Things zc does *better* than crush (keep, don't regress)
- **Frame batch-draining** (`waitForFrame`, 512-frame collapse) — crush needs pubsub throttling instead; zc's is simpler and effective.
- **Version-drift warning** against the pinned letta-code prefix — crush has no equivalent problem, but the pattern is sound.
- **Approval recovery from `pending_control_requests`** on reconnect — recently fixed in zc; crush's local arch never loses these.
- **Empty-conversation honesty** ("(empty conversation — no messages yet)" vs silent blank) and history-fetch error surfacing.
- **Deliberate pre-tea background-color resolution** — crush arrived at the same conclusion (their `capabilities.go` queries are gated); zc's comment trail is better.

---

## Suggested execution order

Two batches, matching Jack's pain ranking:

**Batch A — input feel (items 1,2,3,4,8 + paste):** DynamicHeight, history machine, esc chain, focus/scroll-key gating, x/ansi truncation sweep, PasteMsg handling. Everything is localized to `main.go` key handling + two helpers; the tmux gauntlet scenarios: soft-wrap growth, paste CRLF, recall-edit-recall, esc-with-dropdown, ctrl+u while typing.

**Batch B — transcript calm (items 5,6,7 + §3.4):** gutter grammar + label removal + 120 cap, toast channel, reasoning fold, Edit-card diffs from stored DiffPreview, ingest-truncation removal, streammd incremental promotion + tests.

Deferred but recorded: diffview lift, per-item focus/expansion, approval grace + allow-for-session (shared with gap-audit backlog), turn footer timer, contextual help hints.
