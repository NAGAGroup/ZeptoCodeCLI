# ZeptoCodeCLI Parity & Quality Tracker

Consolidated from three 2026-07-16 audits — **every finding is a line item; nothing was triaged away**:
- `audit-letta-gaps.md` (letta-code 0.28.8 functionality zc hasn't captured) → source tag `letta-gaps`
- `audit-crush-ux.md` (crush UI/UX worth leveraging) → source tag `crush-ux`
- `audit-crush-vs-zc-quality.md` (features both have, crush does better) → source tag `crush-vs-zc`

**How to use**: check items off as they ship; items are numbered `P1…` for cross-reference from the execution batches at the bottom. Tags: `wire` = app-server protocol; `local` = shell-out / local-file pattern; `client` = pure client-side. `lift` = copy crush code nearly verbatim; `adopt` = reimplement pattern; `rewrite` = replace zc's approach. `🐛` = live bug.

**Strategy (decided 2026-07-16)**: incremental + verbatim lifts — NO crush fork-as-base. Crush's standalone packages (`diffview`, `anim`, `notification`, `completions`, `list`, `clipboard`, `ansi16`, `history.go`, `csync`/`ansiext`) get **lifted**, not reimplemented; coupled parts adopt crush's exact logic with its source as the open reference. Crush is FSL-1.1-MIT and zc is private — copying is permitted.

---

## 1. Correctness & recovery

- [ ] **P1 pending-approval replay** — consume `device_status.pending_control_requests` so approvals pending at (re)connect aren't invisible/hung. ⚠️ letta-gaps says missing, crush-vs-zc §4.4 says "recently fixed" — reconcile in zc source first, then close or fix. (letta-gaps + crush-vs-zc; wire; adopt; hours)
- [ ] **P2 `sync` lightweight reconnect** — use `{type:"sync", recover_approvals, force_device_status}` for socket-drop-with-live-server instead of full `runtime_start` respawn. (letta-gaps; wire; adopt; 0.5d)
- [ ] **P3 `abort_message` ack** — send `request_id`, consume `abort_message_response{aborted}` for deterministic esc-interrupt feedback. (letta-gaps; wire; adopt; hours)
- [ ] 🐛 **P4 x/ansi truncation sweep** — `compactOneLine`/`truncateLines`/`shortID` cut by bytes (mid-rune mojibake, CJK width as `len()`); `overlay.render` truncates *already-styled* strings mid-SGR → color bleed; `toolParams` byte budget; statusline title truncation. Replace all with `ansi.Truncate`/`ansi.StringWidth`; grep every `[:n]`/`len(` in render paths. (crush-vs-zc; client; rewrite; hours)
- [ ] 🐛 **P5 no IO in Update** — `/jobs` does a synchronous 2s broker HTTP GET + `loadJobs()` disk walk on the UI thread (`overlay.go:173-201`). Move to `tea.Cmd`. Codify the rule. (crush-vs-zc; client; rewrite; hours)
- [ ] 🐛 **P6 ANSI bleed in tool bodies** — raw SGR in tool output leaves unbalanced color state inside the dim rail. `ansi.Strip` or `RemapANSI16` (see P57). (crush-vs-zc; client; lift; hours)
- [ ] 🐛 **P7 approval-modal ctrl+c** — quits instantly without double-press arming, unlike the main path; mid-turn + modal is the worst accidental-quit moment. (crush-vs-zc; client; rewrite; hours)
- [ ] 🐛 **P8 content-hash cache keys** — `entryCacheKey` uses `len(e.text)`; safe only while entries are append-only. Switch to FNV-64 with length-prefix framing (crush `fnvFields`). (crush-vs-zc + crush-ux; client; adopt; hours)
- [ ] 🐛 **P9 viewport jump on ctrl+r/ctrl+o while scrolled** — item heights change under the viewport; `offsetLine` exceeds new height for a frame. Re-anchor like crush's `SetSize` AtBottom check. (crush-vs-zc; client; adopt; hours)
- [ ] **P10 cwd signals** — consume `cwd_revision` (monotonic; detect rejected stale cwd), `boot_working_directory`, and `get_cwd_map` for correct per-conversation `/cd` display. (letta-gaps; wire; adopt; 0.5d)
- [ ] **P11 permission-mode assumption sweep** — wire has exactly 3 modes (`standard|acceptEdits|unrestricted`); verify no "4 modes"/plan-mode assumption survives anywhere in zc or docs. (letta-gaps; client; verify; minutes)
- [ ] **P12 conversation-switch alert** — notice when the conversation was switched by another client (zc + native TUI may share an agent). (letta-gaps; client; adopt; hours)
- [ ] **P13 busy guards** — gate agent/conversation switching, picker rename/delete, and `/tidy` on `turnActive()` with a warning toast (crush refuses destructive session ops while the agent runs). (crush-ux + crush-vs-zc; client; adopt; hours)

## 2. Input & editor

- [ ] **P14 DynamicHeight** — delete `autosizeInput` (hard-`\n` counting; soft-wrap and paste leave height stale); enable textarea `DynamicHeight` + Min/MaxHeight; adopt `updateTextareaWithPrevHeight` idiom at all ~8 programmatic mutation sites. (crush-vs-zc; client; rewrite; hours)
- [ ] **P15 history state machine** — port crush `model/history.go` (~180 lines): draft preservation, edits-exit-history, cursor-position-gated up/down (works inside recalled multi-line text), esc restores draft; persist to `~/.letta/zc-history.jsonl`. (crush-vs-zc + crush-ux; client; lift; 1d)
- [ ] **P16 esc priority chain** — rewrite: completion dropdown open → hide; history-nav → restore draft; input non-empty → clear (stash to one-slot kill buffer); else → abort turn. Today esc-with-dropdown wipes the whole input. (crush-vs-zc; client; rewrite; hours)
- [ ] **P17 focus routing / stop stealing editor keys** — global ctrl+u/ctrl+d/pgup/pgdn shadow textarea readline bindings while typing. Minimum: gate on empty input; right fix: tab-toggled editor↔transcript focus. Also: real `textinput` (cursor + paste) for overlay filter fields. (crush-vs-zc; client; adopt; 0.5–2d)
- [ ] **P18 `tea.PasteMsg` handling** — normalize `\r\n`, insert via prev-height path, refresh completions. Today paste falls through untreated (CR artifacts sent to the agent). (crush-vs-zc; client; adopt; hours)
- [ ] **P19 paste intelligence** — oversized paste (lines/cols thresholds, 5MB cap, MIME sniff) → `paste_N.txt` attachment (temp-file + @-mention until wire attachments exist); pasted existing-image-path detection; ctrl+v clipboard-image read. (crush-ux; client; adopt; 1d staged)
- [ ] **P20 image paste wire path** — native flow: clipboard image → `[Image #N]` placeholder → resize (sharp/imagemagick fallback) → image content part in `MessageCreate`; large text pastes as `[Pasted text #N +X lines]` display/actual separation; HEIC support exists tool-side. (letta-gaps; client+wire; adopt; 1–2d)
- [ ] **P21 completions overhaul** — anchor-index tracking (`@` at start/after whitespace, filter from anchor; fixes @-dead-in-multiline and mid-buffer accept deleting trailing text), enter = insert+close / tab = insert+keep-open, tiered ranking (exact > name prefix > path segment > fuzzy), match-char highlighting, x/ansi-safe size clamps. (crush-vs-zc + crush-ux; client; adopt/lift; 1d)
- [ ] **P22 Update-tail allow-listing** — forward only KeyPress/Paste/textarea-internal msgs to the input; the KeyReleaseMsg swallow stays as belt-and-braces but stops being load-bearing. (crush-vs-zc; client; adopt; hours)
- [ ] **P23 external `$EDITOR`** — open editor seeded with current input via `x/editor`; pick a binding (ctrl+o is taken by tool expand). (crush-ux; client; lift; hours)
- [ ] **P24 trailing `\` → newline** — strip and insert newline on send. (crush-vs-zc; client; adopt; minutes)
- [ ] **P25 typed `exit`/`quit` intercept** — open quit confirm instead of sending to agent. (crush-vs-zc; client; adopt; minutes)
- [ ] **P26 prompt-gutter mode icons** — `>` normal, red `!` badge + "Yolo"-style placeholder for unrestricted, etc.; color-blind-friendly secondary signal to the border color. (crush-ux; client; adopt; hours)
- [ ] **P27 randomized mode-reactive placeholder** — cosmetic. (crush-vs-zc; client; adopt; minutes)
- [ ] **P28 attachments strip** — chips above input; ctrl+r delete mode (digit deletes one, `r` all, esc cancels). Blocked on attachments (P92). (crush-ux; client; adopt; later)
- [ ] **P29 bash mode (`!`)** — local shell in the *conversation's* cwd (`device_status.current_working_directory`); input locked while running, ctrl+c interrupts, backspace-on-empty exits, shared history; ShellItem transcript card (left bar, last-10 collapsed, h-scroll, exit code, ANSI16-remapped output, replay-persisted); output injected as `<bash-input>/<bash-output>` system-reminder prefix on next message; BashPreview-style syntax-highlighted command display. (letta-gaps + crush-ux; client; adopt; 1–2d)
- [ ] **P30 Ctrl+D queue defer** — hold queued user messages instead of auto-flushing (`○` vs `>` bullets). (letta-gaps; client; adopt; 0.5d)

## 3. Transcript & rendering

- [ ] **P31 transcript grammar rewrite (de-clutter spec)** — implement crush-vs-zc §3.3: drop `you ▸`/`● agent` label lines; user = colored left border on **every** line (per-line, not prefix-then-wrap); assistant = plain 2-col inset; glamour-render user text; uniform 2-cell gutter rhythm; drop per-card key-hint suffixes; optional gap=0 within tool-call runs. (crush-vs-zc; client; rewrite; 1–2d)
- [ ] **P32 120-col prose cap** — `min(width-2, 120)` on all prose renderers; diffs remain the deliberate full-width exception. (crush-ux + crush-vs-zc; client; adopt; minutes)
- [ ] **P33 turn footer** — dim `◇ model · via provider · 12.3s` after each assistant turn (AssistantInfoItem; needs global turn timer — shared with P66). Useful history metadata with per-conversation model switching. (crush-ux; client; adopt; hours)
- [ ] **P34 reasoning fold + thinking windowing** — merge consecutive reasoning bursts into one entry (today: one placeholder per burst); collapsed line or dim inset block; crush's 3-state windowing (last-10 → last-200 tail → full) + "Thought for Xs" footer, space/click cycles. (crush-vs-zc + crush-ux; client; adopt; 1d)
- [ ] **P35 transient feedback → toasts** — mode/model/title/answered/reconnect/abort events become TTL'd statusline messages (5s; error/warn/info styling); errors stay in the transcript. Single biggest de-clutter after P31. (crush-vs-zc + crush-ux; client; adopt; 1d)
- [ ] **P36 streammd v2** — incremental boundary promotion (render only the delta chunk and glue, O(delta) not O(prefix)); whole-prefix hazard scan (loose lists, HTML block openers, link-ref definitions; fix the 200-char window and `---` thematic-break over-match); second cache instance for the thinking stream; port crush's table-driven boundary/prefix/incremental test matrix (zc's streammd has zero tests). (crush-ux + crush-vs-zc; client; adopt; 1–2d)
- [ ] **P37 per-width glamour memo** — `map[int]*TermRenderer` keyed by width (resize churns widths) + "quiet" colorless variant for thinking; per-renderer mutexes (goldmark BlockStack is stateful). (crush-ux; client; adopt; hours)
- [ ] **P38 nested subagent tool tree** — stop dropping subagent stream_deltas; parent Agent card embeds children as compact one-liners in a `lipgloss/v2/tree` with live spinner, result markdown on finish; child-ID→parent-index mapping (`UpdateNestedToolIDs`); parent bumps version on child ticks. Includes the `Compactable`/`SpinningFunc` renderer interfaces. (crush-ux; client; adopt; 2–3d)
- [ ] **P39 TodoWrite / Plan / worktree renderers** — live `☒/☐` checkbox list with in-progress highlight; PlanRenderer; `enter_worktree` result renderer; crush's todos card (`N/M · current task`, "just completed X" phrasing) is the render reference. Data already arrives in stream deltas. (letta-gaps + crush-ux; client; adopt; 1d)
- [ ] **P40 TrajectorySummary** — collapsed "Worked for 3m · 12 steps" turn summaries. (letta-gaps; client; adopt; hours)
- [ ] **P41 canceled-turn marker** — muted "Canceled" line appended on abort (composition-key style cache invalidation). (crush-ux; client; adopt; minutes)
- [ ] **P42 transcript item focus & selection** — tab-focusable transcript, selectable items; enabler for per-item expansion (P54), c/y copy, and clean key routing (P17). Biggest structural UX adoption. (crush-vs-zc; client; adopt; 2d)
- [ ] **P43 mouse text selection + copy** — click-drag with line/col ranges, double-click word, triple-click line (400ms), c/y copy via clipboard pkg + OSC52 fallback, esc clears; manual hit-testing from layout rects (crush proves bubblezone unnecessary). Solves the shift+drag mouse-capture trade-off. (crush-ux; client; adopt; 3d+)
- [ ] **P44 token smoothing** — evaluate native's `use-token-smoothing` pacing if zc streaming feels bursty. (letta-gaps; client; evaluate; hours)
- [ ] **P45 user-message attachment chips** — render attachments as ■/≡/▲ icon chips in user entries. Blocked on P92. (crush-ux; client; adopt; later)

## 4. Tool output & diffs

- [ ] **P46 store full, truncate at render** — kill the 31-line/6000-byte ingest cap (data is *gone*; ctrl+o can never show more; byte cap cuts mid-rune); collapsed = last 10 lines + "… (N hidden)"; per-item expansion (needs P42) with global ctrl+o kept as expand-all; expanded headers wrap instead of truncate. (crush-vs-zc; client; rewrite; 1d)
- [ ] **P47 Edit diffs in tool cards** — keep `req.Request.Diffs` on the entry at approval time and render the colorized diff in the transcript card (today the diff exists only in the modal, then is discarded; completed edits show 4 dim lines). Denied edits: WARN tag above the diff so you still see what would have changed; MultiEdit "N of M edits failed" note. (crush-vs-zc; client; adopt; hours)
- [ ] **P48 lift `diffview`** — crush's standalone unified+split diff package (chroma inside diff lines, per-line bg, intraline word-level via go-udiff, line numbers, hunk separators, x/y scroll, xxh3 caches, golden tests; deps: chroma/udiff/x-ansi/lipgloss only). Route approval modal, Edit cards, and the memory-repo pager's flat `diffColorize` through it. (crush-ux + crush-vs-zc; client; lift; 1–2d)
- [ ] **P49 Edit tool_return structured-diff check** — ask Ecosystem Expert whether letta's Edit returns structured old/new metadata (covers edits that never hit an approval modal). (crush-vs-zc; verify; minutes)
- [ ] **P50 Read/View bodies as editor excerpts** — chroma syntax highlighting + line numbers starting at the request's offset param; image results as `Loaded Image → mime size` line. (crush-ux; client; adopt; 1d)
- [ ] **P51 header/param scheme** — uniform `[]string{main, k1, v1, …}` params across renderers; `minSpaceForMainParam=30` guarantee (kv extras never squeeze the filename); expanded mode hard-wraps params with aligned continuation indent; `PrettyPath` (~-shortening) everywhere. (crush-ux + crush-vs-zc; client; adopt; hours)
- [ ] **P52 inline error visibility** — ERROR/WARN tag + first line of the error in the collapsed card (not hidden in the body); "denied" gets WARN, not ERROR. (crush-vs-zc; client; adopt; hours)
- [ ] **P53 empty-result suppression** — `HasEmptyResult()` → header only, no empty rail block; bash "no output" sentinel suppressed; grep/glob kv params (`include=`, `literal=`). (crush-vs-zc + crush-ux; client; adopt; minutes)
- [ ] **P54 per-card pending spinner + elapsed** — scramble spinner in pending tool headers (replaces static `●`), elapsed time; visible-only animation ticks. Depends on P66. (crush-ux; client; adopt; hours)
- [ ] **P55 hook indicator line** — compact "hooks ran: names + decisions" on tool cards, if/when hook events become visible to zc (see P89). (crush-ux; client; adopt; later)
- [ ] **P56 approval `blocked_path` + preview modes** — surface which permission path tripped and the `fallback|unpreviewable` reasons instead of blank previews. (letta-gaps; wire; adopt; hours)
- [ ] **P57 lift `RemapANSI16`** — rewrite SGR 30–37/90–97 (+256-color 0–15) in captured output to theme truecolor; use for bash tool bodies, `!` shell output, jobs logs. (crush-ux; client; lift; hours)

## 5. Approvals & permissions

- [ ] **P58 approval-dialog grace period** — absorb keystrokes 200ms-quiet/1.5s-max on async open; skip grace when same dialog reopens within 500ms. Kills "typed into the modal and approved Bash by accident". (crush-ux + crush-vs-zc; client; adopt; hours)
- [ ] **P59 approval power keys** — a/s/d direct hotkeys (allow / allow-for-session / deny), unified↔split diff toggle `t` (auto-split ≥140 cols), fullscreen `f` (forced under min window size), h-scroll (shift+arrows), left/right option buttons + ctrl+y confirm; golden tests. (crush-ux; client; adopt; 1–2d)
- [ ] **P60 AskUserQuestion inline-at-editor** — question replaces the input area instead of a modal mid-stream (InlineEditor pattern; suppresses pills, tab still toggles focus); answered questions render into the transcript as Q/A cards. (crush-ux; client; adopt; 1–2d)

## 6. Statusline, chrome & notifications

- [ ] **P61 git branch in statusline** — `device_status.git_context {branch, recent_branches[]}`; free upgrade, pairs with P83. (letta-gaps; wire; adopt; hours)
- [ ] **P62 rich loop status** — render all 8 `update_loop_status` states (RETRYING_API_REQUEST, WAITING_ON_APPROVAL, WAITING_ON_INPUT, …), adopt `executing_tool_call_ids` (self-healing vs pairing start/end events), `retry` delta → attempt/delay countdown line, `loop_error.is_terminal` fatal-vs-recoverable, `status` deltas (info/success/warning — provider-fallback visibility for free). (letta-gaps; wire; adopt; 1d)
- [ ] **P63 `should_doctor` hint** — one-line "memory needs /doctor" nudge under input. (letta-gaps; wire; adopt; minutes)
- [ ] **P64 focus-aware notifications** — OSC 99 (with `p=?` capability query) / OSC 777 / bell fallback chain, SSH-aware, fires only when terminal unfocused; on approval-needed and turn-complete; native OS layer later. Lift crush `ui/notification`. (crush-ux + letta-gaps; client; lift; 1d)
- [ ] **P65 contextual help hints** — delete the hardcoded statusline hint string; render `helpModel.ShortHelpView(keys.ShortHelp())` so hints change with focus/state (zc already has keymap.go + help.Model unused here). (crush-vs-zc + crush-ux; client; adopt; hours)
- [ ] **P66 lift `anim` spinner + turn timer** — scramble/gradient spinner, 20 FPS step-driven (deterministic), staggered births, prerendered frame cache, `NoScramble` mode; **dynamic suffix func fed by global StartTurn/StopTurn timer → "Thinking… 12s"**; visible-only ticking + restart-on-scroll. (crush-ux; client; lift; hours)
- [ ] **P67 todo/queue pills** — pills above input (queue count as gradient `▶▶▶`, todo pill with live spinner + activeForm), ctrl+t expansion, auto-expand on tall terminals, focusable expanded panel; badge queue items by `update_queue` kind/source (message/task_notification/cron_prompt × user/cron/subagent/channel); `remove_queue_item` for "load queued message back into input" editing. (crush-ux + letta-gaps; client+wire; adopt; 1–2d)
- [ ] **P68 mod panels, native-side** — render known mods' panels from their local ui-state files (muscle-memory pattern; zc's jobs panel already does this); file the upstream feature request for a wire panel-frame extension (server evaluates `openPanel` renders, ships lines) — no protocol exists at 0.28.8 (`panels: false` in listener mod-adapter). (parent-verified; client; adopt; ~1d/mod + FR)
- [ ] **P69 compaction UX** — CompactingAnimation-equivalent during /compact + context-% warning color ramp. (letta-gaps; client; adopt; hours)
- [ ] **P70 context chart** — full `context_window_overview` breakdown (system/core/summary/functions/messages) + per-turn token sparkline with compaction marks; **verify wire availability first** (native fetches via SDK). (letta-gaps; local?; adopt; 1d + verify)
- [ ] **P71 ExitStats** — session duration/tokens/cost/agent + pin hint on quit; zc exits cold today. (letta-gaps; client; adopt; hours)
- [ ] **P72 tabbed HelpDialog** — Commands ⇄ Shortcuts tabs, j/k navigation, custom commands with source/namespace annotations (pairs with P85). (letta-gaps; client; adopt; 1d)
- [ ] **P73 upgrade nudge** — on detected letta-code version change: "letta-code upgraded under you — re-verify protocol" (Jack's protocol-instability rule), analogous to native's release-notes-once. (letta-gaps; client; adopt; hours)
- [ ] **P74 scrollbar auto-hide** — appears on scroll, TTL hide (sequence-numbered cmd to avoid races), 1-col reserve only when shown; `always/default/never` config. (crush-ux; client; adopt; hours)
- [ ] **P75 service status rows** — online/busy/error/disabled icon rows (crush MCP/LSP pattern) for zc's broker/jobs panel. (crush-ux; client; adopt; hours)
- [ ] **P76 dialog stack** — push/pop/BringToFront overlay stack, opaque `Action` returns (dialogs never mutate app state), `LoadingDialog` spinner interface; zc is single-modal today. (crush-ux; client; adopt; 1–2d)
- [ ] **P77 palette/picker polish** — responsive info-column auto-hide (>25%/35% row-width rules), radio-tab grouping, argument follow-up form for commands with args, verify zc renders `mod_commands[].args` hints. (crush-ux + letta-gaps; client; adopt; 1d)
- [ ] **P78 model-picker chain** — provider grouping headers, recently-used hoisting, reasoning badges, and the seamless "pick model → no key → inline API-key entry with live validation → resume selection" flow bridging /model + /connect. (crush-ux; client; adopt; 1–2d)
- [ ] **P79 conversation picker in-place ops** — inline rename (question_editor row swap), delete/archive with y/n confirm, busy guard (P13), right-aligned auto-hiding timestamps. Wire: `conversation_update`. (crush-ux; client+wire; adopt; 1d)
- [ ] **P80 landing view** — post-picker pre-chat screen: agent info, cwd, model, memory/skills/mods status columns. Later nicety. (crush-ux + crush-vs-zc; client; adopt; 1d)
- [ ] **P81 theme scalability** — quickstyle "semantic slots → style tree" builder (~30 slots incl. ANSI-16 mapping), `Blend1D` gradient helpers for dialog titles/spinner/pills, centralized icon consts. (crush-ux; client; adopt; 1d)
- [ ] **P82 capabilities struct** — generalize zc's pre-tea background probe into a Capabilities detection pass (kitty graphics, sixel, cell size, OSC99, focus events; env-gated). Prereq for P64/P92. (crush-ux; client; adopt; 1d)
- [ ] **P83 `/branch` picker** — `search_branches` (name/is_current/is_remote, filtered, cwd-scoped) + `checkout_branch` (with create flag); pairs with P61. (letta-gaps; wire; adopt; 1d)
- [ ] **P84 ZEPTO wordmark header** — optional whimsy; crush's `logo` pkg (letterform gradients, `╱` filler, compact variant) is standalone-liftable. (crush-ux; client; lift; hours; optional)

## 7. Commands & features

- [ ] **P85 custom slash commands** — `.commands/*.md` (project) + `~/.letta/commands/*.md` (user); frontmatter `description`/`argument-hint`, subdirectory namespacing, project-shadows-user; body becomes the prompt. Dotfiles-alignable. (letta-gaps; client; adopt; 1d)
- [ ] **P86 `/btw`** — `conversation_fork {hidden:true}` → `input` on the forked runtime scope → deltas filtered by conversation → ephemeral side pane (idle/forking/streaming/complete; jump-to-conversation or dismiss). Wire-ready today; the old deferral premise (single-seat) was wrong — the constraint is per control channel, not per turn. (letta-gaps; wire; adopt; 1–2d)
- [ ] **P87 `/stream` toggle** — render live deltas vs only-on-stop_reason. (letta-gaps; client; adopt; hours)
- [ ] **P88 `/download`** — AgentFile `.af` export via headless shell-out. Low value; parity item. (letta-gaps; local; adopt; hours)
- [ ] **P89 client-loop hooks** — SessionStart / Notification / UserPromptSubmit hooks appear to run only in the native client loop, never for zc sessions (PreToolUse/PostToolUse still run in the listener). **Verify with Ecosystem Expert**, then run these events client-side from the merged settings zc already reads for /hooks. (letta-gaps; client; verify+adopt; 1d)
- [ ] **P90 `/palace`** — `xdg-open` the Memory Palace web UI; pairs with P91. (letta-gaps; client; adopt; minutes)
- [ ] **P91 memory links view** — `list_memory {include_references:true}` returns `[[path]]` reference edges; render a links/graph view in /memory. (letta-gaps; wire; adopt; 1d)
- [ ] **P92 attachments & images end-to-end** — wire image content parts (P20) + file picker with inline image preview (kitty/half-block via `ui/image`), clipboard PNG (lift `internal/clipboard`), attachment chips (P28/P45). (crush-ux + letta-gaps; client+wire; adopt/lift; 2–3d)
- [ ] **P93 `create_agent` over wire** — first-class creation flow (personality presets memo|tutorial|blank|linus|kawaii, model, tags, pin) e.g. "new agent" in the picker; replaces the `node -e` preset extraction for *creation* (keep it for /system + /personality content). Also `runtime_start.create_agent` with `memfs:false` for throwaway workers. (letta-gaps; wire; adopt; 1d)
- [ ] **P94 `enable_memfs` via wire** — official wire path for enabling; keep settings edits only for disable/aliases (avoids the settings-cache race where possible). (letta-gaps; wire; adopt; hours)
- [ ] **P95 `/unpin` behavior check** — verify zc's /pin toggles both ways; else add. (letta-gaps; client; verify; minutes)
- [ ] **P96 `conversation_titles` experiment suggestion** — surface as a recommended default in /experiments (zc's statusline shows titles). (letta-gaps; client; adopt; minutes)
- [ ] **P97 `search_files` for @-completion** — evaluate the mtime-ranked server-side substring search (what powers native @-completion) vs zc's local dir listing; server-consistent and rank-aware. (letta-gaps; wire; evaluate; hours)

## 8. Performance & rendering infra

- [ ] **P98 list cache v2** — items expose `Version() uint64` (bump on every observable mutation incl. spinner ticks) + `Finished() bool`; list memo keyed (ptr, width, version); finished items **frozen** (verbatim emit, no Render call); `Prewarm(from,batch)` incremental warming; `Overflows(h)` bottom-walk instead of O(N) TotalHeight; selection-drag freeze suppression. (crush-ux; client; adopt; 2–3d)
- [ ] **P99 resize settle + draw cache** — `BeginResize()` flag (skip scrollbar/total-height, reflow visible only) + settle timer + `WarmStep` batched prewarm; separate draw-level cache of the ANSI-decoded ScreenBuffer keyed by rendered string (byte-identical frames skip reparse). Targets the known resize-under-streaming risk; port crush's `resize_bench_test.go` benchmark pattern. (crush-ux; client; adopt; included in P98's 2–3d)
- [ ] **P100 mouse-flood input filter** — `tea.WithFilter` coalescing wheel/motion to ~60Hz with delta accumulation + sign-flip cancel, upstream of the existing batch-drain. (crush-ux; client; adopt; hours)
- [ ] **P101 per-section render caches** — split assistant entries into thinking/content/error sections cached independently (FNV-keyed) + `compositionKey` for structural decisions; per-line string-prefix focus styling (never lipgloss re-render of long messages). Relevant once P31/P34 land. (crush-ux; client; adopt; 1d)
- [ ] **P102 component golden tests + zc AGENTS.md** — catwalk-style golden files for renderers + benchmarks for hot paths; codify crush's rules for zc (single top-level model, imperative children, IO only in Cmd, x/ansi only, width accounting, item-owned caching). (crush-ux; client; adopt; 1d)
- [ ] **P103 util lifts as needed** — `csync` (typed concurrent Map/Slice), `ansiext`, `stringext` when their call sites arrive. (crush-ux; client; lift; opportunistic)

## 9. Bigger bets

- [ ] **P104 external client-registered tools** — `runtime_start.external_tools[]` (JSON-schema defs, optional hidden scope_id) → `external_tool_call_request/response` (MCP-style content), per-turn `client_tool_allowlist`/`external_tool_scope_ids`; zc-side tools like "ask user via picker", "notify". Opens a whole capability class. (letta-gaps; wire; adopt; 2–3d)
- [ ] **P105 terminal PTY channel** — `terminal_spawn(cols,rows,cwd)` → bidirectional `terminal_input`/`terminal_output` + resize/kill/exited; conversation-cwd aware. Effort is mostly the terminal-emulator widget. (letta-gaps; wire; adopt; big)
- [ ] **P106 find-in-files pane** — `grep_in_files` (regex/case/whole-word/glob, line+column, context_lines, 500-match cap) — IDE-grade, far beyond /search's transcript scan. (letta-gaps; wire; adopt; 2–3d)
- [ ] **P107 file viewer pane** — `watch_file`/`unwatch_file` + `file_changed` push + read_file/get_tree; future surface. (letta-gaps; wire; adopt; big, later)

## Do-not-regress (zc already better than crush — keep)

- **Frame batch-draining** (`waitForFrame`, 512-frame collapse) — simpler and effective vs crush's pubsub throttling.
- **Version-drift warning** against the pinned letta-code prefix.
- **Approval recovery via `pending_control_requests`** — per crush-vs-zc §4.4 already present; reconcile with P1 and keep whichever is true.
- **Empty-conversation honesty** ("(empty conversation)") + history-fetch error surfacing — never a silent blank.
- **Pre-tea background-color resolution** (one-shot, pinned style) with its comment trail.
- **Content-change-gated `refreshCompletions`** (the blink-tick/key-release fix) and the raw-tab swallow.
- **Optimistic permission-mode cycling** carried across conversation switches.

## Out of scope (decided — visible decisions, not omissions)

- **Mod panels over the wire** — no protocol exists at 0.28.8 (zero panel frames; listener sets `panels:false`). Native-side rendering per mod is P68; the wire extension is an upstream feature-request candidate.
- **/login, /logout** — Jack dropped; native CLI owns auth.
- **Channels surface** — for now (~20 channel_* wire commands unconsumed by decision).
- **Alias commands** (no-alias rule): /resume /chdir /exit /set-max-context /reflection /link /unlink /ade.
- **/pinned** (pickers already ★-sort), **/install-github-app** (niche; native handles), **/reflect-arena** (skip for now — noted for Jack's reflection interests), **/permissions** (no longer exists natively), **/terminal** (kitty protocol already gives shift+enter), **WindowTitlePicker**.
- **OAuth/onboarding dialogs** and `chatgpt_usage_read` (zc filters OAuth providers out of /connect).
- **`file_ops` CRDT channel** — Desktop collaborative-editor machinery; heavy.
- **`acting_user_id`** — cloud attribution; N/A on local backend.
- **`change_device_state` default agent/conversation binding** — desktop concern; note only.
- **Ultraviolet ScreenBuffer migration** — take the draw-cache idea (P99) without the framework.
- **SQLite persistence / sessions / LSP / MCP internals** — server-side in zc's architecture.
- **Hyper credits / provider theme switching** — crush-business-specific; the invalidation mechanism (theme key → rebuild styles → clear item + markdown caches) is noted for any future theme switching.
- **bubblezone** — crush proves manual hit-testing suffices; one less dep.
- **Sidebar layout** — zc deliberately keeps a bottom statusline; crush's fair dynamic-height allocator is noted as a reusable algorithm for multi-section panels (see P75).

---

## Suggested execution order (every item appears; ordering is Jack's call)

1. **Batch 1 — correctness & live bugs**: P1 P2 P3 P4 P5 P6 P7 P8 P9 P10 P11 P12 P13 P56
2. **Batch 2 — input feel** (Jack's #1 pain): P14 P15 P16 P17 P18 P21 P22 P24 P25 P58 (grace, protects the modal while input changes land) P23 P26 P27
3. **Batch 3 — transcript calm** (Jack's #2 pain): P31 P32 P33 P34 P35 P41 P37 P36 P47 P46 P48 P49 P50 P52 P53 P51 P57
4. **Batch 4 — status & notification feel**: P61 P62 P63 P64 P65 P66 P54 P69 P73 P74 P75 P96
5. **Batch 5 — wire adoption quick wins**: memory/skills/crons refresh pushes (P108 — see note), P67 P30 P94 P95 P97
6. **Batch 6 — features**: P29 P85 P86 P87 P38 P39 P40 P68 P70 P71 P72 P90 P91 P83 P93 P89 P55 P88
7. **Batch 7 — rendering infra**: P98 P99 P100 P101 P102 P103 P44
8. **Batch 8 — dialogs & chrome**: P76 P59 P60 P77 P78 P79 P80 P81 P82 P84
9. **Batch 9 — selection & mouse**: P42 P43 P9-followups, P74 refinements
10. **Batch 10 — bigger bets**: P19 P20 P92 P28 P45 P104 P105 P106 P107

> **P108 live overlay refresh** — consume `memory_updated {affected_paths[]}`, `skills_updated`, `crons_updated` push frames: auto-refresh /memory, /skills, /crons views instead of stale-until-reopen, plus a transcript/toast "✦ memory updated: system/persona.md" indicator. (letta-gaps; wire; adopt; 0.5d) — *numbered late to avoid renumbering; belongs conceptually in section 6 and executes in Batch 5.*
