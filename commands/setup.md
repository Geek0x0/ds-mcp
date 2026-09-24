---
description: Verify the subagent-mcp installation (binary on PATH, config file loads, active provider's env_key is set)
allowed-tools: Bash
---

Verify the local subagent-mcp installation and print a compact status report.

Never print the value of an API key: report only the variable name and whether it
is set. Do not run `env`, `printenv`, `set`, or similar commands that would dump
the environment.

Run these checks with Bash:

1. Locate and identify the binary:

   ```bash
   command -v subagent-mcp && subagent-mcp --version
   ```

   If the binary is missing, tell the user to run `go install ./cmd/subagent-mcp`
   from the subagent-mcp repository root and make sure `$(go env GOPATH)/bin` is
   on PATH.

2. Locate the config file, read the active provider and its `env_key` variable
   name, and check that the variable is set without printing its value:

   ```bash
   config="${SUBAGENT_MCP_CONFIG:-$HOME/.config/subagent-mcp/config.toml}"
   echo "config file: $config"

   if [ -f "$config" ]; then
     active="$(sed -n 's/^[[:space:]]*active_provider[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$config" | head -1)"
     env_key="$(awk -v provider="$active" '
       $0 ~ "^[[:space:]]*\\[providers\\." provider "\\][[:space:]]*$" { inside = 1; next }
       $0 ~ "^[[:space:]]*\\[" { inside = 0 }
       inside && $0 ~ "^[[:space:]]*env_key[[:space:]]*=" {
         line = $0
         sub(/^[[:space:]]*env_key[[:space:]]*=[[:space:]]*"/, "", line)
         sub(/".*$/, "", line)
         print line
         exit
       }' "$config")"
     echo "active provider: ${active:-UNKNOWN}"
     echo "env_key variable: ${env_key:-UNKNOWN} (name only; value never printed)"
     if [ -n "$env_key" ]; then
       if [ -n "${!env_key:-}" ]; then
         echo "env_key status: $env_key set"
       else
         echo "env_key status: $env_key missing"
       fi
     fi
   fi
   ```

3. Verify that the config file loads, by starting the server with stdin at EOF.
   It validates the whole config before serving anything and exits at EOF. A
   missing key is a separate failure: its message says the variable "must be
   set to the API key" and never includes the key value.

   ```bash
   if output="$(subagent-mcp </dev/null 2>&1)"; then
     echo "config status: OK"
   elif printf '%s' "$output" | grep -q 'must be set to the API key'; then
     echo "config status: OK (the file loads; the active provider key is not set)"
   else
     echo "config status: FAILED"
     printf '%s\n' "$output" | sed 's/^/  /'
     echo "hint: compare the named field with config.example.toml from the"
     echo "subagent-mcp repository"
   fi
   ```

4. If the config file is missing, tell the user to copy `config.example.toml`
   from the subagent-mcp repository to the path from step 2, set the active
   provider's `env_key`, and reconnect the MCP server.

Then report:

- subagent-mcp binary: <absolute path and version, or MISSING with the install hint>
- config file: <path, and OK, MISSING with the copy hint, or FAILED with the named field>
- active provider: <name from active_provider, or UNKNOWN>
- env_key: <variable name and set/missing — never its value>
- ready: <when the binary, config, and env_key all pass: the MCP tools are
  available once the server is connected — `subagent` and `subagent-reply` by
  default, or the names set by SUBAGENT_MCP_TOOL_NAME; otherwise the exact
  step to fix>
