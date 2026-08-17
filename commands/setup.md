---
description: Verify the ds-mcp installation (binary on PATH, credentials configured via DEEPSEEK_API_KEY or auth.json)
allowed-tools: Bash
---

Verify the local ds-mcp installation and print a compact status report.

Run these checks with Bash:

1. `command -v ds-mcp` — locate the binary. If missing, tell the user to run
   `go install ./cmd/ds-mcp` from the ds-mcp repository root and make sure
   `$(go env GOPATH)/bin` is on PATH.
2. `test -n "$DEEPSEEK_API_KEY" && echo set || echo missing` — check the
   environment credential.
3. Check the fallback credential file and its permissions:
   ```bash
   auth_file="$HOME/.config/ds-mcp/auth.json"
   if test -f "$auth_file"; then
     auth_mode="$(stat -c '%a' "$auth_file")"
     case "$auth_mode" in
       [0-7]00|[0-7][0-7]00) echo "present ($auth_mode, secure)" ;;
       *) echo "present ($auth_mode, insecure)" ;;
     esac
   else
     echo missing
   fi
   ```
   The server accepts the file only when it has no group or other permissions;
   `chmod 600 ~/.config/ds-mcp/auth.json` is the standard fix.

Then report:
- ds-mcp binary: <absolute path, or MISSING with the install hint>
- DEEPSEEK_API_KEY: <set or not set>
- auth.json: <MISSING, or present with its mode and secure/insecure status>
- credentials: <CONFIGURED via DEEPSEEK_API_KEY if set; otherwise CONFIGURED via
  auth.json if present and secure; otherwise auth.json INSECURE with the
  `chmod 600` hint if present; otherwise MISSING with a hint to export the
  environment variable or create the auth file with mode 600>
- If the binary is found and either credential source passes, the `deepseek` MCP
  server is ready; its tools `deepseek` and `deepseek-reply` are available once
  the server is connected.
