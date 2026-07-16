# ZeptoCodeCLI

A Go/BubbleTea TUI frontend for [Letta Code](https://github.com/letta-ai/letta-code).

Architecture: **thin client over the Letta Code app-server WebSocket protocol** (`protocol_v2`).
The app-server owns the entire harness — agent loop, tool execution, permissions, mods —
and this TUI only renders protocol frames and answers approval prompts. Management
commands (skill installs, provider setup, etc.) stay with the native `letta` CLI.

## Status

Milestones 1–5 complete: a full-featured chat cockpit.

- **Chat**: glamour markdown, streaming, collapsed reasoning (`ctrl+r`),
  tool cards with output previews (`ctrl+o`), queue + subagent activity lines
- **Approvals**: modal with diff previews, permission suggestions (number keys
  allow + persist a rule), approval recovery on resume
- **Navigation**: conversation picker/switcher (`ctrl+p`), history replay on
  resume, agent picker when `--agent` omitted, `--agent` by name or id
- **Commands**: slash-command palette (`ctrl+k`) exposing server built-ins and
  mod commands (ZeptoCode's `/audit`, `/jobs`, …), `/cmd` dispatch with tab
  completion, `@`-path tab completion
- **Modes**: `shift+tab` cycles standard → acceptEdits → unrestricted live
  (`change_device_state`), statusline always shows server truth
- **Resilience**: app-server death detection with auto-respawn + conversation
  resume; letta-code version drift warning at startup
- **ZeptoCode integration**: native jobs/broker panel (`ctrl+j`) reading
  `~/.letta/jobs` manifests and the broker `/status` endpoint
- **Portability**: `GOOS=windows` cross-compiles (untested on a real Windows box)

```bash
# The TUI (omit --agent for an interactive picker)
pixi run zc -- [--agent <name-or-id>] [--conversation <id>] [--mode standard]

# Protocol frame dumper (debugging)
pixi run spike -- --agent <agent-id> [--message "..."]

# Headless end-to-end tests against a real app-server
ZC_SMOKE_AGENT=<agent-id> pixi run smoke
```

Keys: `enter` send · `alt+enter` newline · `up`/`down` input history · `tab`
complete `/commands` and `@paths` · `esc` clear input / abort turn ·
`ctrl+k` command palette · `ctrl+p` conversations · `ctrl+j` jobs/broker ·
`ctrl+r` reasoning · `ctrl+o` tool output · `shift+tab` permission mode ·
`a`/`d` approve/deny · `1`–`9` approve + persist suggestion ·
wheel/`pgup`/`pgdn` scroll · `ctrl+c` quit (double-press mid-turn).

Note on approvals: letta-code's server default permission mode is
`unrestricted`; pass `--mode standard` if you want interactive tool approvals.
Read-only shell commands are auto-allowed server-side in any mode. The
`requires_approval` stop_reason is *not* terminal — the server emits it just
before the `can_use_tool` control_request and resumes the turn after the
client's decision.

## Requirements

- [pixi](https://pixi.sh) (provides the Go toolchain)
- `letta` (Letta Code CLI) on PATH — the app-server we spawn is `letta app-server`

## Protocol compatibility

There is no wire-level protocol version negotiation in the app-server protocol.
This client pins a tested range of `@letta-ai/letta-code` releases; on upstream
bumps, diff the shipped `.d.ts` protocol types
(`@letta-ai/letta-code/app-server-protocol`) for an exact changelog.
