---
description: Verify the ds-mcp installation (binary on PATH, DEEPSEEK_API_KEY set)
allowed-tools: Bash
---

Verify the local ds-mcp installation and print a compact status report.

Run these checks with Bash:

1. `command -v ds-mcp` — locate the binary. If missing, tell the user to run
   `go install ./cmd/ds-mcp` from the ds-mcp repository root and make sure
   `$(go env GOPATH)/bin` is on PATH.
2. `test -n "$DEEPSEEK_API_KEY" && echo set || echo missing` — check the API key.
   If missing, tell the user to export `DEEPSEEK_API_KEY` in the environment that
   launches Claude Code.

Then report:
- ds-mcp binary: <absolute path, or MISSING with the install hint>
- DEEPSEEK_API_KEY: <set, or MISSING with the export hint>
- If both pass: the `deepseek` MCP server is ready; its tools `deepseek` and
  `deepseek-reply` are available once the server is connected.
