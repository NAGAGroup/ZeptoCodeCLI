# ZeptoCodeCLI

A Go/BubbleTea TUI frontend for [Letta Code](https://github.com/letta-ai/letta-code).

Architecture: **thin client over the Letta Code app-server WebSocket protocol** (`protocol_v2`).
The app-server owns the entire harness — agent loop, tool execution, permissions, mods —
and this TUI only renders protocol frames and answers approval prompts. Management
commands (skill installs, provider setup, etc.) stay with the native `letta` CLI.

## Status

Milestone 1: interactive chat loop.

```bash
# The TUI: streaming transcript, input box, statusline, tool-approval modal
pixi run zc -- --agent <agent-id> [--conversation <id>] [--mode standard]

# Protocol frame dumper (debugging)
pixi run spike -- --agent <agent-id> [--message "..."]

# Headless end-to-end tests against a real app-server
ZC_SMOKE_AGENT=<agent-id> pixi run smoke
```

Keys: `enter` send · `alt+enter` newline · `esc` abort turn · `a`/`d` approve/deny
tool use · `ctrl+c` quit.

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
