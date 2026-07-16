# ZeptoCodeCLI Go Thin-Client Rewrite Spec

**Status**: Draft 1 (2026-07-16)
**Depends on**: `docs/protocol/SPEC.md` (protocol), `letta ui-server` (fork, Stages 0-5 done)
**Governing principle**: SPEC.md §1 — *client renders, EVERYTHING else is constructed state served over the protocol; every full-protocol TUI behaves EXACTLY the same; zero zc-flavored semantics.*

---

## 1. Objective

Rewrite ZeptoCodeCLI's Go TUI as a **pure rendering/interaction thin client** over the `letta ui-server` stdio protocol. All state and policy — command implementations, FS scans, settings edits, search, reflect, jobs, tidy, resolution — moves to the TS runtime below the seam. Go keeps only terminal truth.

**Stop bar**: the Go binary spawns `bun ui-server.ts`, renders what it's told, sends semantic input events, and has no domain logic about letta-code internals. zc's custom Go command implementations are gone. Native behavior is total.

---

## 2. Current state inventory

**9,562 lines** across 18 Go files (excluding pixi/go-toolchain):

| File | Lines | Role |
|---|---|---|
| `cmd/zc/main.go` | 2,366 | TUI model, Update/View, key handling, frame dispatch, slash command routing, approval modal, transcript rendering |
| `cmd/zc/mgmt.go` | 2,388 | Management command implementations (/secret, /skills, /connect, /memory, /mods, /hooks, /profiles, /crons, /tidy, /search, /reflect, /system, /personality, /pin, /memfs, /compaction, /experiments, /sleeptime, /description, /recompile, /context, /bg) |
| `internal/client/client.go` | 742 | WebSocket transport: spawn app-server, control+stream channels, request/response correlation, runtime_start, all wire commands |
| `internal/client/mgmt.go` | 413 | WS-based management API methods (secret, skills, connect, memory, crons, agent retrieve) |
| `internal/protocol/protocol.go` | 632 | Frame type definitions (protocol_v2 shapes) |
| `internal/protocol/mgmt.go` | 395 | Management frame type definitions |
| `internal/ui/form/form.go` | 521 | Sequential field-page forms (select/multiselect/input/text/confirm) |
| `internal/ui/list/list.go` | 254 | Lazy transcript list with per-item line memo |
| `cmd/zc/overlay.go` | 233 | Overlay/picker component |
| `cmd/zc/toolcard.go` | 183 | Tool call/return card rendering |
| `cmd/zc/completion.go` | 171 | Tab completion (commands + @paths) |
| `cmd/zc/question.go` | 147 | AskUserQuestion form dialog |
| `cmd/zc/theme.go` | 158 | Adaptive color palette |
| `cmd/zc/streammd.go` | 105 | Streaming markdown with glamour cache |
| `cmd/zc/pager.go` | 95 | Modal scrollable viewer |
| `cmd/zc/dialog.go` | 80 | Centered modal dialog |
| `cmd/zc/keymap.go` | 73 | Keybinding definitions |
| `cmd/stdio-smoke/main.go` | 90 | Integration test (already exists, proven) |
| `internal/client/stdio.go` | 317 | Stdio transport skeleton (already exists, proven) |

**Client API surface today**: 40+ methods on `*Client` (WS-based): StartRuntime, ListAgents, MessagesList, ConversationsList, ExecuteCommand, ChangeMode, SwitchConversation, SwitchAgent, Fork, UpdateConversation, UpdateAgent, ListModels, UpdateModel, ChangeCWD, SendUserMessage, RespondApproval, Abort, AgentRetrieve, SecretList, SecretApply, SkillEnable, SkillDisable, ListConnectProviders, ConnectProvider, DisconnectProvider, ChatGPTUsage, MemoryList, MemoryRead, MemoryWrite, MemoryDelete, MemoryHistory, MemoryCommitDiff, MemoryFileAtRef, EnableMemfs, CronList, CronRuns, CronTrigger, CronDelete, Experiments, SetExperiment, ReflectionSettings, SetReflectionSettings, RecompileConversation.

**Client-side commands today** (`clientCommands` in main.go): /agents, /conversations, /jobs, /new, /mode, /model, /fork, /title, /rename, /cd, /usage, /export, /subagents, /help, /quit. These are Go-native implementations that call Client methods.

**Management commands today** (mgmt.go, 2,388 lines): /secret, /skills, /connect, /memory, /memory-repository, /mods, /hooks, /mcp, /profiles, /crons, /tidy, /description, /recompile, /context, /bg, /search, /reflect, /system, /personality, /pin, /memfs, /compaction, /experiments, /sleeptime. Each is a Go function that does FS scans, shell-outs, settings file mutations, or wire calls — all of which should be server-side.

---

## 3. What stays in Go (terminal truth)

Per SPEC.md §6, the client owns:

### 3.1 Rendering
- **Transcript list** (`internal/ui/list/`): entry rendering, line memoization, follow mode, scrollback
- **Tool cards** (`cmd/zc/toolcard.go`): icon, humanized name, params header, railed body, expand
- **Streaming markdown** (`cmd/zc/streammd.go`): prefix-cache glamour, boundary detection, finish render
- **Dialogs/modals** (`cmd/zc/dialog.go`): centered overlays for approvals, questions, confirmations
- **Forms** (`internal/ui/form/`): sequential field pages for pickers, inputs, confirms
- **Overlay/picker** (`cmd/zc/overlay.go`): fuzzy-filtered selection list
- **Pager** (`cmd/zc/pager.go`): scrollable viewer for diffs, memory files, help text
- **Theme** (`cmd/zc/theme.go`): adaptive palette, input border color = permission mode
- **Statusline**: mode pill, agent name, conversation title, model — all from `device_status` data

### 3.2 Input
- **Textarea**: cursor, wrapping, kill-ring, undo, input history recall
- **Completion UI** (`cmd/zc/completion.go`): floating popup, fuzzy match on served items
- **Key handling** (`cmd/zc/keymap.go`): keybindings, esc priority chain, ctrl+c double-press
- **Paste handling**: normalize `\r\n`, prev-height path (TODO: P18)
- **Clipboard**: copy selection, paste

### 3.3 Terminal control
- Alt-screen, mouse capture, OSC sequences (bell, clipboard, cwd reporting)
- Window title
- Resize handling
- Stdio pipe management (spawn/kill the ui-server child process)

### 3.4 Minimal local state
- Edit buffer content (text being typed)
- Scroll position
- Overlay/form UI state (which picker is open, filter text, selection cursor)
- Input history (up/down recall — persisted to `~/.letta/zc-history.jsonl`)
- Ctrl+c armed timestamp (double-press quit)
- Reasoning/tool-output toggle preferences (show/hide)

**Nothing else.** No agent state, no conversation state, no settings, no command implementations, no FS scans, no file reads, no shell-outs, no JSONL parsing, no preset evaluation.

---

## 4. What gets gutted (moved below the seam)

### 4.1 Client-side command implementations → DELETE

**All 15 `clientCommands` in main.go are deleted.** Their functions become server-side:

| Go command | Server-side replacement | Protocol frame |
|---|---|---|
| `/agents` | Agent picker overlay data | `agents_list` → overlay descriptor (§5.5) |
| `/conversations` | Conversation picker overlay data | `conversations_list` → overlay descriptor |
| `/jobs` | Jobs panel (native below seam) | `mod_panels_query` (jobs mod panel) or native `/jobs` via `execute_command` |
| `/new` | Create + switch conversation | `conversation_create` → `change_device_state` |
| `/mode` | Permission mode cycling | `change_device_state {mode}` |
| `/model` | Model picker overlay data | `models_list` → overlay descriptor; `update_model` |
| `/fork` | Fork conversation | `conversation_fork` |
| `/title` | Set conversation title | `conversation_update {title}` |
| `/rename` | Rename agent | `agent_update {name}` |
| `/cd` | Change working directory | `change_device_state {cwd}` |
| `/usage` | Usage stats display | `session_stats` / `usage_statistics` |
| `/export` | Export agent | `agent_export` |
| `/subagents` | Subagent list | `subagent_defs_list` |
| `/help` | Help overlay | Client-side (pure rendering of keymap) — stays |
| `/quit` | Quit | Client-side (kill child + exit) — stays |

**Net**: `/help` and `/quit` stay client-side (pure terminal operations). Everything else is a frame round-trip.

### 4.2 Management commands (mgmt.go, 2,388 lines) → DELETE ENTIRE FILE

Every management command becomes either:
1. **A frame round-trip** (data → overlay/pager rendering)
2. **An `execute_command` dispatch** (server runs the native command, output arrives as `command_result`)

| mgmt.go function | Lines | Replacement |
|---|---|---|
| `cmdSecret` / `openSecretEditForm` / `applySecret` | ~90 | `secret_list` + `secret_apply` frames → form overlay |
| `cmdSkills` / `scanSkills` / `skillDescription` | ~170 | `skills_list` frame → overlay; enable/disable via `execute_command` |
| `cmdConnect` / `providersOverlay` / `openProviderForm` | ~200 | `list_connect_providers` + `connect_provider` frames → form overlay |
| `cmdMemory` (list/view/write/rm/enable) | ~110 | `list_memory` + `read_memory_file` + `execute_command` for write/rm/enable |
| `cmdMemoryRepository` | ~70 | `memory_repository_status` / `memory_repository_push` frames |
| `cmdMods` / `scanMods` | ~100 | `mod_registry` frame → overlay; `/mods reload` via `mods_reload` |
| `cmdHooks` / `renderHooksView` | ~90 | `hooks_list` frame → pager; `hooks_update` for edits |
| `cmdMCP` | ~20 | Honest stub (no local backend support — stays honest) |
| `cmdProfiles` / `loadProfiles` / `saveProfile` / `deleteProfile` | ~90 | `settings_read` + `settings_patch` frames |
| `cmdCrons` / `resolveCronRef` | ~130 | `cron_list` frame → overlay; trigger/delete via frames |
| `cmdTidy` | ~60 | `execute_command` (server-side) or new `tidy_conversations` frame |
| `cmdDescription` / `cmdRecompile` | ~40 | `agent_update` / `conversation_recompile` frames |
| `cmdContext` | ~40 | `context_window_overview` frame |
| `cmdBg` | ~30 | `device_status.background_processes` (already in device_status) |
| `cmdSearch` / `extractTextBlocks` | ~130 | `search_messages` frame → overlay |
| `cmdSystem` / `applySystemPreset` / `presetEval` | ~60 | `execute_command` for /system (server runs preset eval) |
| `cmdPersonality` / `confirmPersonality` | ~50 | `execute_command` for /personality |
| `cmdPinWith` / `pinnedAgentIDs` | ~50 | `settings_read` + `settings_patch` |
| `cmdReasoningTab` | ~30 | `settings_patch` |
| `cmdMemfs` | ~30 | `execute_command` for /memfs or `settings_patch` |
| `cmdCompaction` | ~50 | `agent_update` with compaction_settings |
| `cmdExperiments` / `renderExperiments` | ~40 | `execute_command` for /experiments |
| `cmdSleeptime` | ~60 | `execute_command` for /sleeptime |
| `mutateGlobalSettings` / `setAgentSettingsFlag` / `mutateAgentSettingsEntries` | ~60 | `settings_patch` frame |
| `globalSettingsPath` / `lettaPackageDir` / `bundledSkillsDir` | ~30 | DELETE (server knows these paths) |

**Net**: `cmd/zc/mgmt.go` is deleted entirely (~2,388 lines removed). The slash command dispatcher in main.go becomes a simple lookup: if it's not /help or /quit, send it as `input_submit` with the raw `/command args` string and let the server route it. The server's native command registry handles it.

### 4.3 Client transport → SWAP

**Delete**: `internal/client/client.go` (742 lines, WS transport) and `internal/client/mgmt.go` (413 lines, WS management methods).

**Keep**: `internal/client/stdio.go` (317 lines, stdio transport) — expand it to cover the full API surface.

The StdioClient needs to grow the same public method set as the WS Client, but every method becomes a stdio frame round-trip:

```go
// Instead of WS runtime_start, send hello and parse hello_response
func ConnectStdio(ctx, opts) (*StdioClient, error)

// Every WS command method becomes a frame send:
func (c *StdioClient) SendMessage(content string) error           // input_submit
func (c *StdioClient) SendInterrupt() error                       // control_request{interrupt}
func (c *StdioClient) SendApprovalResponse(reqID, behavior, updatedInput) error
func (c *StdioClient) QueryPanels(ctx, width) ([]Panel, error)    // mod_panels_query
func (c *StdioClient) ExecuteCommand(cmdID, args string) error    // execute_command
func (c *StdioClient) ChangeMode(mode) error                      // change_device_state{mode}
func (c *StdioClient) ChangeCWD(path string) error                // change_device_state{cwd}
func (c *StdioClient) ListAgents(ctx) ([]Agent, error)            // agents_list
func (c *StdioClient) ListConversations(ctx, agentID) ([]Conv, error) // conversations_list
func (c *StdioClient) ListModels(ctx) (string, error)             // models_list
func (c *StdioClient) UpdateModel(ctx, payload) error             // update_model
func (c *StdioClient) Fork(ctx) (string, error)                   // conversation_fork
func (c *StdioClient) UpdateConversation(ctx, body) error         // conversation_update
func (c *StdioClient) UpdateAgent(ctx, body) error                // agent_update
func (c *StdioClient) ListMessages(ctx) ([]Message, error)        // conversation_messages_list
func (c *StdioClient) SearchMessages(ctx, query) ([]Result, error) // search_messages
func (c *StdioClient) ListSkills(ctx) ([]Skill, error)            // skills_list
func (c *StdioClient) ListSecrets(ctx) ([]Secret, error)          // secret_list
func (c *StdioClient) ApplySecret(ctx, key, value) error          // secret_apply
func (c *StdioClient) ReadSettings(ctx) (map[string]any, error)   // settings_read
func (c *StdioClient) PatchSettings(ctx, patch) error             // settings_patch
func (c *StdioClient) ListHooks(ctx) (Hooks, error)               // hooks_list
func (c *StdioClient) UpdateHooks(ctx, hooks) error               // hooks_update
func (c *StdioClient) ListCrons(ctx) ([]Cron, error)              // cron_list
func (c *StdioClient) ReadMemoryFile(ctx, path) (string, error)   // read_memory_file
func (c *StdioClient) ListMemory(ctx) ([]FileEntry, error)        // list_memory
func (c *StdioClient) MemRepoStatus(ctx) (bool, error)            // memory_repository_status
func (c *StdioClient) MemRepoPush(ctx) error                      // memory_repository_push
func (c *StdioClient) StartReflection(ctx) error                  // start_reflection
func (c *StdioClient) ListSubagents(ctx) ([]Subagent, error)      // subagent_defs_list
func (c *StdioClient) ModRegistry(ctx) (Registry, error)          // mod_registry
func (c *StdioClient) ModsReload(ctx) error                       // mods_reload
func (c *StdioClient) AgentExport(ctx, agentID) (json.RawMessage, error) // agent_export
func (c *StdioClient) AgentRetrieve(ctx, agentID) (Agent, error)  // agent_retrieve
func (c *StdioClient) Recompile(ctx) error                        // conversation_recompile
func (c *StdioClient) SearchFiles(ctx, pattern) ([]string, error) // search_files
func (c *StdioClient) ReadFile(ctx, path) (string, error)         // read_file
func (c *StdioClient) ContextWindow(ctx) (ContextInfo, error)     // context_window_overview
func (c *StdioClient) SessionStats(ctx) (Stats, error)            // session_stats
```

### 4.4 Protocol types → SIMPLIFY

`internal/protocol/protocol.go` (632 lines) and `internal/protocol/mgmt.go` (395 lines) define frame types for the WS protocol_v2 shapes. Under the new protocol:
- Many frames are new (not borrowed from protocol_v2) — new type definitions needed
- Some borrowed shapes carry over (per SPEC.md §4)
- The `RuntimeScope` struct changes (no separate control/stream channels)

**Approach**: keep the existing protocol types for borrowed shapes; add new types for the §5 frames. Delete types for frames that no longer exist (e.g. `sync`, `abort_message` if replaced by `control_request{interrupt}`).

---

## 5. Frame-to-UI mapping

How each inbound protocol frame maps to Go rendering:

### 5.1 Transcript frames (§5.1)

| Frame | Go rendering action |
|---|---|
| `transcript_update {chunk}` | Append/update entry in the transcript list. Chunk types: `text` → append to assistant entry; `tool_call` → create/update tool card; `tool_return` → fill tool card body; `thinking` → reasoning entry; `stop_reason` → close streaming entry; `usage` → update statusline tokens |
| `transcript_sync {entries[]}` | Replace transcript with the accumulated entry model. Each entry has `kind` (user/assistant/tool_call/tool_return/thinking/shell/event/error) and rendered text. This is the authoritative state — the list re-renders from this |
| `turn_start` | Start spinner, set `turnActive=true`, disable input send |
| `turn_end {stop_reason}` | Stop spinner, set `turnActive=false`, enable input, refresh statusline |

### 5.2 Approval frames

| Frame | Go rendering action |
|---|---|
| `control_request {can_use_tool}` | Push to `approvals` FIFO; render head as modal dialog with tool name, args, diff previews, permission suggestions. Client sends `control_response` on user decision |
| `control_request {ask_user_question}` | Render as question form dialog. Client sends `control_response` with `updated_input` containing answers |

### 5.3 Device status

| Frame | Go rendering action |
|---|---|
| `device_status` | Update: `mode` → input border color + statusline pill; `cwd` → statusline; `model` → statusline; `agent_name` → statusline; `conversation_id` → conversation context; `tools` → completion data; `command_catalog` → slash palette data; `background_processes` → /bg overlay; `pending_control_requests` → replay approval modals; `git_context` → statusline branch |

### 5.4 Push frames (unsolicited)

| Frame | Go rendering action |
|---|---|
| `settings_updated` | Invalidate local settings cache (if any); re-read on next access |
| `mods_updated` | Refresh mod panel data on next `mod_panels_query` |
| `memory_updated` | Refresh memory list on next /memory access |
| `skills_updated` | Refresh skills list on next /skills access |
| `conversations_updated` | Refresh conversation list on next picker open |
| `notification` | Emit terminal signal (bell/OSC) if client is unfocused; show toast |
| `should_doctor` | Show doctor warning in statusline |

### 5.5 Overlay frames (§5.5 — pickers, forms, viewers)

| Frame | Go rendering action |
|---|---|
| `overlay_open {descriptor}` | Render overlay per descriptor type: `select` → overlay list; `multiselect` → overlay list with checkboxes; `form` → sequential form pages; `confirm` → yes/no dialog; `viewer` → pager modal |
| `overlay_update {items, cursor}` | Update overlay items/filter/selection |
| `overlay_close` | Close overlay, return to transcript |

The client sends `overlay_event` frames back: `select`, `confirm`, `cancel`, `filter`, `cursor_move`.

### 5.6 Command execution

| Frame | Go rendering action |
|---|---|
| `command_result {success, output}` | Append as info/error entry in transcript |
| `bash_output {output}` | Render as shell transcript entry |

---

## 6. Slash command dispatch (simplified)

**Current** (main.go `dispatchSlashCommand`, ~50 lines + 2,388 lines in mgmt.go):
1. Check `clientCommands` — if match, run Go implementation
2. Check `knownCommands` (server-supported) — if match, send `execute_command`
3. Check skills — if match, send "load skill X" message
4. Otherwise: error

**New** (simplified):
1. `/help` → client-side (render keymap) — stays
2. `/quit` → client-side (kill child + exit) — stays
3. Everything else → send as `input_submit {content: "/command args"}` — server routes it

The server's `input_submit` handler already routes `/`-prefixed content through the native command registry (Stage 2). For interactive commands that need overlays, the server sends `overlay_open` descriptors. For data-fetching commands (like /agents), the server can either:
- Execute the command and send `command_result` with text output, OR
- Send an `overlay_open` descriptor with the data for a rich picker

This means **the Go side doesn't need to know what commands exist**. The `command_catalog` in `device_status` provides autocomplete data, but the actual execution is always server-side.

---

## 7. Model struct changes

### 7.1 Fields that stay

```go
type model struct {
    // Transport (swapped)
    sc     *client.StdioClient  // replaces *client.Client
    
    // Rendering (stay)
    list   *list.List
    input  textarea.Model
    width  int
    height int
    
    // Transcript state (driven by transcript_sync)
    entries       []*entry
    openAssistant *entry
    openReasoning *entry
    toolByCallID  map[string]*entry
    
    // UI state (stay — pure presentation)
    approvals     []*protocol.ControlRequest
    overlay        *overlay
    completion     completionState
    question       *questionForm
    pager          *pager
    helpModel      help.Model
    showHelp       bool
    spin           spinner.Model
    showReasoning  bool
    showToolOutput bool
    
    // Terminal state (stay)
    mode       protocol.PermissionMode  // from device_status
    connected  bool
    quitting   bool
    ctrlCArmed time.Time
    
    // Input history (stay — client-owned)
    history    []string
    historyIdx int
    
    // Statusline data (from device_status)
    serverCWD   string
    modelHandle string
    agentName   string
    convTitle   string
    
    // Startup phase machine (stay)
    phase      int
    startupErr string
}
```

### 7.2 Fields that are DELETED

```go
// DELETE — server-side now:
cli             *client.Client      // replaced by sc *StdioClient
port            int                 // no port (stdio)
serverLog       *os.File            // stderr pipe, handled by StdioClient
logPath         string              // no server log path
mgmt            *mgmtForm           // management forms → overlay descriptors
memoryDir       string              // server knows this
serverVersion   string              // from hello_response
versionWarned   bool                // from hello_response
reconnectTried  bool                // no reconnect (stdio: child dies = exit)
switching       bool                // server handles switch state
bgProcs         []protocol.BackgroundProcessSummary // from device_status
modelID         string              // from device_status
reasoningTabCycle bool              // settings_patch
statuslineTemplate string           // settings_read
markdown        *glamour.TermRenderer // stays (rendering)
markdownWidth   int                 // stays (rendering)
loopStatus      string              // from transcript frames
queue           []protocol.QueueItem // from transcript frames
subagents       []protocol.SubagentSnapshot // from subagent_defs_list
supportedCmds   []string            // from command_catalog
modCmds         []protocol.ModCommandInfo // from command_catalog
seenApprovals   map[string]bool     // from device_status.pending_control_requests
lastUsage       protocol.Delta      // from session_stats
sessionTokens   int64               // from session_stats
spinning        bool                // derived from turn_active
lastModalH      int                 // rendering detail
startOpts       client.Options      // replaced by StdioOptions
```

**Estimated field reduction**: ~20 fields deleted, ~15 stay. The model struct shrinks significantly.

---

## 8. File-by-file disposition

| File | Lines | Disposition | Action |
|---|---|---|---|
| `cmd/zc/main.go` | 2,366 | **HEAVILY EDITED** | Swap `*Client` → `*StdioClient`; delete `clientCommands` (keep /help, /quit); simplify `dispatchSlashCommand` to send everything as `input_submit`; delete client-side picker data fetching (agents/conversations/models come from frames); delete `handleFrame` cases for dead frames; keep rendering, input, key handling |
| `cmd/zc/mgmt.go` | 2,388 | **DELETE** | Entire file gone. All management commands are server-side. |
| `internal/client/client.go` | 742 | **DELETE** | WS transport replaced by stdio. |
| `internal/client/mgmt.go` | 413 | **DELETE** | WS management methods replaced by stdio frames. |
| `internal/client/stdio.go` | 317 | **EXPAND** | Add all the methods listed in §4.3. |
| `internal/protocol/protocol.go` | 632 | **EDIT** | Remove dead frame types; add new §5 frame types. |
| `internal/protocol/mgmt.go` | 395 | **DELETE or MERGE** | Management frame types merge into protocol.go (one protocol now). |
| `internal/ui/form/form.go` | 521 | **KEEP** | Pure rendering. |
| `internal/ui/list/list.go` | 254 | **KEEP** | Pure rendering. |
| `cmd/zc/overlay.go` | 233 | **KEEP** | Pure rendering. May need `overlay_update` frame handling. |
| `cmd/zc/toolcard.go` | 183 | **KEEP** | Pure rendering. |
| `cmd/zc/completion.go` | 171 | **KEEP** | Pure rendering. Data from `command_catalog` + `search_files`. |
| `cmd/zc/question.go` | 147 | **KEEP** | Pure rendering. |
| `cmd/zc/theme.go` | 158 | **KEEP** | Pure rendering. |
| `cmd/zc/streammd.go` | 105 | **KEEP** | Pure rendering. |
| `cmd/zc/pager.go` | 95 | **KEEP** | Pure rendering. |
| `cmd/zc/dialog.go` | 80 | **KEEP** | Pure rendering. |
| `cmd/zc/keymap.go` | 73 | **KEEP** | Pure rendering. |
| `cmd/stdio-smoke/main.go` | 90 | **KEEP** | Integration test. |

**Line count projection**:
- **Before**: 9,562 lines
- **Deleted**: ~3,543 lines (mgmt.go + client.go + client/mgmt.go)
- **Added**: ~400 lines (expanded stdio.go + new protocol types)
- **After**: ~6,400 lines (33% reduction)

The remaining code is **pure rendering and input** — no domain logic, no FS access, no shell-outs, no settings file parsing.

---

## 9. Startup flow (new)

```
1. Parse args (--agent, --conversation, --cwd)
2. StdioClient.ConnectStdio()
   → spawn `bun ui-server.ts --agent <id> [--conversation <id>] [--cwd <path>]`
   → send hello frame
   → receive hello_response (agent_id, conversation_id, model, version)
   → receive device_status (mode, cwd, tools, command_catalog)
3. If no --agent: agent picker
   → send agents_list → overlay_open{select} → user picks → reconnect with chosen agent
4. If no --conversation: conversation picker
   → send conversations_list → overlay_open{select} → user picks → reconnect
5. Enter main loop (phaseChat)
   → read frames → render
   → read keypresses → send input events
```

**No runtime_start frame** — the hello handshake IS the runtime start. The server creates the conversation and starts the agent loop as part of the hello response.

---

## 10. Error handling and lifecycle

### 10.1 Child process death
- StdioClient detects stdout EOF → `Frames` channel closed → model receives `disconnectedMsg`
- No reconnect logic (per SPEC.md R9: stdio has no reconnect — child dies = session ends)
- Go exits with the child's exit code

### 10.2 Stdin EOF (user closed terminal)
- Go's stdin closes → Go kills child process → Go exits

### 10.3 Ctrl+C
- First press: arm ctrl+c, show "press again to quit" in statusline
- Second press: kill child, exit
- During approval modal: arm ctrl+c (P7 fix), second press quits

### 10.4 Esc interrupt
- During active turn: send `control_request{interrupt}` → server aborts → `turn_end{stop_reason: "cancelled"}`
- During overlay/form: close overlay (client-side, no frame)
- During input with completion: hide completion (client-side)

---

## 11. Migration plan (ordered)

### Phase 1: Transport swap (mechanical)
1. Expand `internal/client/stdio.go` with all §4.3 methods
2. Add new protocol frame types to `internal/protocol/protocol.go`
3. Swap `model.cli` (`*client.Client`) → `model.sc` (`*client.StdioClient`)
4. Update all method calls in main.go to use StdioClient
5. Update startup flow (hello handshake replaces runtime_start)
6. Verify: basic chat works (send message, receive transcript, approval modal)

**Deliverable**: zc runs on stdio, no WS. All existing features still work (just through stdio frames).

### Phase 2: Gut client commands
1. Delete `clientCommands` in main.go (keep /help, /quit)
2. Simplify `dispatchSlashCommand` to send everything as `input_submit`
3. Verify: slash commands work via server routing

**Deliverable**: no Go-side command implementations.

### Phase 3: Gut management commands
1. Delete `cmd/zc/mgmt.go` entirely
2. Delete `internal/client/mgmt.go` (WS management methods)
3. Update main.go to remove all mgmt.go function references
4. Replace with frame round-trips for any management UI that needs data
5. Verify: /secret, /skills, /memory, /mods, /hooks, /profiles, /crons all work via frames

**Deliverable**: mgmt.go is gone, management commands work through the protocol.

### Phase 4: Clean up protocol types
1. Delete `internal/protocol/mgmt.go` (merge needed types into protocol.go)
2. Remove dead frame types (sync, abort_message if unused, etc.)
3. Add any missing §5 frame types
4. Verify: build clean, all features work

**Deliverable**: protocol types match the spec, no dead code.

### Phase 5: Delete old WS client
1. Delete `internal/client/client.go`
2. Delete `internal/client/mgmt.go`
3. Remove `github.com/coder/websocket` dependency from go.mod
4. Verify: `pixi run build` + `pixi run vet` + `pixi run smoke` + tmux gauntlet

**Deliverable**: WS transport is gone, stdio is the only transport.

---

## 12. What does NOT change

- **Rendering code**: all of `internal/ui/`, `cmd/zc/{toolcard,streammd,dialog,pager,theme,overlay,completion,question,keymap}.go` — pure rendering, untouched
- **Input handling**: textarea, cursor, kill-ring, history recall — stays client-side
- **Keybindings**: keymap.go stays, though some bindings may shift (e.g. /jobs becomes a server command)
- **Theme**: adaptive palette stays
- **Glamour markdown**: streaming cache stays
- **Tmux gauntlet**: still the validation method
- **Validation**: `pixi run build` + `vet` + `smoke` + tmux gauntlet against AppServer Probe Agent

---

## 13. Open questions

| # | Question | Resolution |
|---|---|---|
| G1 | Does the server send `overlay_open` for interactive commands (like /agents), or does the client proactively send `agents_list` when it wants to show a picker? | **Server-driven**: the server sends `overlay_open` descriptors when a command requires user interaction. The client never proactively fetches data — it renders what it's told. This means `/agents` → server sends `overlay_open{select, items: [...]}`. The client just renders it. |
| G2 | How does the client know which commands need overlays vs text output? | It doesn't. The client sends `input_submit{/command}` and the server decides whether to respond with `command_result` (text) or `overlay_open` (interactive). The client handles both. |
| G3 | What about the startup agent/conversation picker (before the first turn)? | The client sends `agents_list` / `conversations_list` directly (these are data-fetch frames, not commands). The picker is client-side presentation over the served items. This is the one exception to "server-driven overlays" — startup pickers are client-initiated because there's no command to trigger them. |
| G4 | Does `mod_panels_query` poll periodically, or is it event-driven? | **Client-polled** (SPEC.md Q-A1): client sends `mod_panels_query{width}` on every render frame where panels are visible. The server evaluates renders and returns lines. This matches native render-on-every-frame semantics. |
| G5 | What happens to `/jobs` (currently a client command that reads local files + HTTP)? | Becomes `execute_command{/jobs}` → server runs the native /jobs mod command → `command_result` with text output. OR: the jobs mod registers a panel → client renders it via `mod_panels_query`. The mod panel approach is better (live updates) but requires the mod to register a panel render function. |
| G6 | Does the client need to handle `transcript_update` chunks AND `transcript_sync` entries? | **Both** (SPEC.md §5.1): `transcript_update` carries streaming deltas (live token-by-token rendering); `transcript_sync` carries the accumulated entry model (authoritative state). The client renders from `transcript_update` during a turn, then reconciles with `transcript_sync` at turn end. |
| G7 | How are approval diff previews served? | The `control_request` frame includes `diff_previews` (borrowed from protocol_v2) — the server computes diffs below the seam. The client renders them in the approval modal. No client-side diff computation. |
| G8 | What about the statusline template (`~/.letta/zc.json`)? | Moves to `settings_read` — the server reads it from the settings file and includes it in `device_status` or a dedicated `statusline_template` field. The client renders the template with substituted values. |

---

## Appendix A: Frame coverage audit

Mapping SPEC.md §5 frames to Go StdioClient methods and main.go handlers:

| SPEC §5 frame | StdioClient method | main.go handler | Status |
|---|---|---|---|
| §5.0 `hello`/`hello_response` | `ConnectStdio` | startup phase | ✅ done |
| §5.1 `transcript_update` | (push frame) | `handleFrame` → list update | ✅ done |
| §5.1 `transcript_sync` | (push frame) | `handleFrame` → list replace | ✅ done |
| §5.1 `turn_start`/`turn_end` | (push frame) | `handleFrame` → spinner | ✅ done |
| §5.2 `input_submit` | `SendMessage` | key handler → send | ✅ done |
| §5.2 bash mode | (server routes `!`) | (server-side) | ✅ done |
| §5.2 slash commands | (server routes `/`) | `dispatchSlashCommand` → send | ✅ done |
| §5.3 `control_request` (approval) | (push frame) | `handleFrame` → modal | ✅ done |
| §5.3 `control_response` | `SendApprovalResponse` | modal → send | ✅ done |
| §5.4 `device_status` | (push frame) | `handleFrame` → statusline | ✅ done |
| §5.5 `overlay_open`/`update`/`close` | (push frame) | `handleFrame` → overlay | ⬜ Phase 2 |
| §5.5 `overlay_event` | `sendOverlayEvent` | overlay handler → send | ⬜ Phase 2 |
| §5.6 `command_catalog` | (in device_status) | completion data | ✅ done |
| §5.6 `execute_command` | `ExecuteCommand` | slash dispatch | ✅ done |
| §5.6 `command_result` | (push frame) | `handleFrame` → entry | ✅ done |
| §5.7 `settings_read`/`patch` | `ReadSettings`/`PatchSettings` | mgmt commands | ⬜ Phase 3 |
| §5.7 `settings_updated` | (push frame) | cache invalidate | ⬜ Phase 3 |
| §5.8 `hooks_list`/`update` | `ListHooks`/`UpdateHooks` | /hooks | ⬜ Phase 3 |
| §5.9 `notification` | (push frame) | bell/OSC | ⬜ Phase 3 |
| §5.9 `session_event` | `sendSessionEvent` | lifecycle | ⬜ Phase 3 |
| §5.10 `skills_list` | `ListSkills` | /skills | ⬜ Phase 3 |
| §5.10 `skills_updated` | (push frame) | cache invalidate | ⬜ Phase 3 |
| §5.11 `search_messages` | `SearchMessages` | /search | ⬜ Phase 3 |
| §5.12 `context_window_overview` | `ContextWindow` | /context | ⬜ Phase 3 |
| §5.12 `session_stats` | `SessionStats` | /usage | ⬜ Phase 3 |
| §5.13 `mod_panels_query`/`response` | `QueryPanels` | statusline/panels | ✅ done |
| §5.14 `mod_registry` | `ModRegistry` | /mods | ⬜ Phase 3 |
| §5.14 `mods_updated` | (push frame) | cache invalidate | ⬜ Phase 3 |
| §5.14 `mods_reload` | `ModsReload` | /mods reload | ⬜ Phase 3 |
| §5.15 `start_reflection` | `StartReflection` | /reflect | ⬜ Phase 3 |
| §5.16 `subagent_defs_list` | `ListSubagents` | /subagents | ⬜ Phase 3 |
| §5.17 `memory_repository_*` | `MemRepoStatus`/`Push` | /memory-repository | ⬜ Phase 3 |
| §5.17 `list_memory`/`read_memory_file` | `ListMemory`/`ReadMemoryFile` | /memory | ⬜ Phase 3 |
| §5.17 `secret_list`/`apply` | `ListSecrets`/`ApplySecret` | /secret | ⬜ Phase 3 |
| §5.17 `cron_list` | `ListCrons` | /crons | ⬜ Phase 3 |
| §5.18 `agent_export` | `AgentExport` | /export | ⬜ Phase 3 |
| §5.18 `agent_retrieve`/`update` | `AgentRetrieve`/`UpdateAgent` | /rename, /description | ⬜ Phase 3 |
| §5.18 `conversation_create`/`fork`/`update`/`messages_list`/`recompile` | various | /new, /fork, /title | ⬜ Phase 3 |
| §5.18 `agents_list`/`conversations_list` | `ListAgents`/`ListConversations` | pickers | ⬜ Phase 3 |
| §5.18 `models_list`/`update_model` | `ListModels`/`UpdateModel` | /model | ⬜ Phase 3 |
| §5.18 `change_device_state` | `ChangeMode`/`ChangeCWD` | /mode, /cd | ✅ done |
| §5.19 `read_file`/`search_files` | `ReadFile`/`SearchFiles` | completion | ⬜ Phase 3 |

**Summary**: 41 frame handlers in the TS server; 12 already wired in Go (the core chat loop), 29 to wire in Phase 3.

---

## Appendix B: Code that becomes trivial

### Slash command dispatch (before: ~50 lines + 2,388 lines in mgmt.go)

```go
// BEFORE: 2,438 lines across two files
func (m *model) dispatchSlashCommand(text string) tea.Cmd {
    name := strings.TrimPrefix(strings.Fields(text)[0], "/")
    // ... 50 lines of clientCommands lookup, knownCommands check, skill scan ...
}

// AFTER: 8 lines
func (m *model) dispatchSlashCommand(text string) tea.Cmd {
    if text == "/help" {
        m.showHelp = true
        return nil
    }
    if text == "/quit" {
        m.quitting = true
        return tea.Quit
    }
    // Everything else: server routes it
    m.closeStreaming()
    m.appendEntry(&entry{kind: entryUser, text: text})
    m.sc.SendMessage(text) // server sees /command and routes
    return nil
}
```

### Agent picker (before: ~30 lines + ListAgents call; after: overlay frame)

```go
// BEFORE: client fetches data, builds overlay items
func (m *model) openAgentPicker() tea.Cmd {
    return func() tea.Msg {
        agents, _ := m.cli.ListAgents(ctx)
        m.overlay = &overlay{kind: overlayAgents, items: agentOverlayItems(agents)}
        return nil
    }
}

// AFTER: server sends overlay_open when /agents is executed
// (handleFrame receives overlay_open → sets m.overlay from descriptor)
// No client-side data fetching.
```
