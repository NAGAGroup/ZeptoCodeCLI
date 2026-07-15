# ZeptoCodeCLI

A Go/BubbleTea TUI frontend for [Letta Code](https://github.com/letta-ai/letta-code).

Architecture: **thin client over the Letta Code app-server WebSocket protocol** (`protocol_v2`).
The app-server owns the entire harness — agent loop, tool execution, permissions, mods —
and this TUI only renders protocol frames and answers approval prompts. Management
commands (skill installs, provider setup, etc.) stay with the native `letta` CLI.

## Status

De-risking spike stage. `pixi run spike -- --agent <agent-id>` spawns a dedicated
app-server on loopback, runs one turn against the given agent in a fresh conversation,
and dumps every protocol frame to stdout.

## Requirements

- [pixi](https://pixi.sh) (provides the Go toolchain)
- `letta` (Letta Code CLI) on PATH — the app-server we spawn is `letta app-server`

## Protocol compatibility

There is no wire-level protocol version negotiation in the app-server protocol.
This client pins a tested range of `@letta-ai/letta-code` releases; on upstream
bumps, diff the shipped `.d.ts` protocol types
(`@letta-ai/letta-code/app-server-protocol`) for an exact changelog.
