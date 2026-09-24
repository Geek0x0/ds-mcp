---
description: Verify the subagent-mcp installation (binary on PATH, config file loads, each provider's key and reachability)
allowed-tools: Bash
---

Verify the local subagent-mcp installation and print a compact status report.

`subagent-mcp --check-config` validates the config file, reports each provider's
`env_key` name and whether it is set, and asks each configured provider for its
model list so a bad key or base URL fails loudly. It never prints key values.

Run these checks with Bash:

1. Locate and identify the binary:

   ```bash
   command -v subagent-mcp && subagent-mcp --version
   ```

   If the binary is missing, tell the user to run `go install ./cmd/subagent-mcp`
   from the subagent-mcp repository root and make sure `$(go env GOPATH)/bin` is
   on PATH.

2. Validate the config file and each provider:

   ```bash
   config="${SUBAGENT_MCP_CONFIG:-$HOME/.config/subagent-mcp/config.toml}"
   echo "config file: $config"
   if subagent-mcp --check-config "$config"; then
     echo "config status: PASS"
   else
     echo "config status: FAILED (exit $?)"
   fi
   ```

   Exit code 0 means every provider whose key is set passed; exit code 1 means
   at least one check failed. A missing key for the active provider is a
   failure; a missing key for any other provider is reported as `not set,
   skipped` and does not affect the exit code. A provider whose API call fails
   is reported as `FAIL` with the error. Warnings that a configured model id is
   not in the provider's current model list do not affect the exit code.

   Do not add `--live` here: it makes real, billed API calls. Mention it only if
   the user asks how to verify a provider end to end.

3. If the config file is missing, tell the user to copy `config.example.toml`
   from the subagent-mcp repository to the path from step 2, set the active
   provider's `env_key`, and reconnect the MCP server.

Then report:

- subagent-mcp binary: <absolute path and version, or MISSING with the install hint>
- config file: <path, and OK, MISSING with the copy hint, or FAILED with the failing check-config line>
- providers: <for each provider in the check-config output: name, active marker,
  key name set or skipped, api OK or FAIL; never key values>
- ready: <when the binary, config, and active provider's key all pass: the MCP
  tools are available once the server is connected — `subagent` and
  `subagent-reply` by default, or the names set by SUBAGENT_MCP_TOOL_NAME;
  otherwise the exact step to fix>
