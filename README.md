# ds-mcp

ds-mcp exposes DeepSeek as a full MCP coding agent with real shell and file-tool execution, rather than acting as a thin passthrough to the DeepSeek chat API.

## Install

1. From the ds-mcp repository root, install the binary:

   ```bash
   go install ./cmd/ds-mcp
   ```

   Make sure `$(go env GOPATH)/bin` is on `PATH`.

2. Add this repository as a Claude Code plugin marketplace:

   ```bash
   claude plugin marketplace add /path/to/ds-mcp
   ```

3. Install the plugin:

   ```bash
   claude plugin install deepseek@ds-mcp
   ```

4. Configure a DeepSeek API key in the environment that launches Claude Code (see [Environment](#environment) for both credential sources), restart or reconnect as needed, then run `/deepseek:setup` to verify the binary and environment.

## Environment

| Variable | Required | Description |
|---|---:|---|
| `DEEPSEEK_API_KEY` | No | DeepSeek API key. When set to a non-empty value, it takes precedence over the auth file. |
| `DEEPSEEK_BASE_URL` | No | API base URL. Defaults to `https://api.deepseek.com`. |

The server checks `DEEPSEEK_API_KEY` first. If it is unset or empty, the server falls back to `~/.config/ds-mcp/auth.json`, which must contain:

```json
{"api_key": "..."}
```

One of these credential sources is required. The auth file must have no group or other access (permissions no more permissive than `0600`); otherwise, the server refuses to start and instructs you to run `chmod 600` on the file. Invalid JSON produces a startup error identifying the file as invalid JSON, and an empty or missing `api_key` produces a startup error identifying that field instead of silently falling through to the generic credential-required error.

## Tools

### `deepseek`

Starts a new coding-agent thread.

| Parameter | Required | Default | Description |
|---|---:|---|---|
| `prompt` | Yes | — | String task prompt for the new thread. |
| `model` | No | `deepseek-v4-pro` | DeepSeek model name. |
| `reasoning-effort` | No | `high` | Reasoning effort: `low` for simple tasks, `high` for typical work, or `max` for the hardest problems. |
| `cwd` | No | Server process working directory | Absolute path to an existing directory. |
| `sandbox` | No | `read-only` | `read-only`, `workspace-write`, or `danger-full-access`. |
| `approval-policy` | No | `on-request` | `untrusted`, `on-request`, `on-failure`, or `never`. |
| `base-instructions` | No | Built-in instructions | Complete replacement for the built-in base system instructions. An empty or omitted value uses the built-in default. |
| `developer-instructions` | No | None | Additional system instructions appended after the selected base instructions. An empty or omitted value appends nothing. |
| `config` | No | `{}` | Loose object. Recognized key: `max_turns`, a number from 1 to 100000. The default is 50; unknown keys and invalid or out-of-range values are silently ignored. |

The response includes `structuredContent.threadId`. Retain it to continue the session. Once a session is created, execution errors also return its `threadId`, so the session remains resumable.

### `deepseek-reply`

Continues an existing coding-agent thread. Session settings cannot be changed on a reply.

| Parameter | Required | Description |
|---|---:|---|
| `threadId` | Yes | String thread ID returned by an earlier `deepseek` or `deepseek-reply` call. |
| `prompt` | Yes | String follow-up prompt for the existing thread. |

Only one call can process a thread at a time. A concurrent reply returns a `busy` error with the same `threadId`. An unknown ID returns `unknown threadId`; check the ID or create a new session if the server has restarted.

## Sandbox and approvals

The sandbox classifies operations as inside or outside its boundary. The approval policy then decides whether the operation runs, asks the MCP client for approval, or is denied.

| Sandbox | `untrusted` | `on-request` | `on-failure` | `never` |
|---|---|---|---|---|
| `read-only` | Allow `read_file` and allowlisted shell; ask for everything else | Allow `read_file` and allowlisted shell; ask for everything else | Same as `on-request` | Allow `read_file` and allowlisted shell; deny everything else |
| `workspace-write` | Allow `read_file` and allowlisted shell; ask for other shell and all `write_file` calls | Allow `read_file`, all shell, and `write_file` inside `cwd`; ask for `write_file` outside `cwd` | Same as `on-request` | Allow `read_file`, all shell, and `write_file` inside `cwd`; deny `write_file` outside `cwd` |
| `danger-full-access` | Allow `read_file` and allowlisted shell; ask for all other operations | Allow all operations | Same as `on-request` | Allow all operations |

The shell allowlist covers `ls`, `cat`, `head`, `tail`, `rg`, `grep`, `find`, `pwd`, `wc`, `stat`, `which`, and `echo`, plus the Git subcommands `status`, `diff`, `log`, `show`, `branch`, `blame`, `rev-parse`, and `ls-files`. Commands with output redirection, command substitution, process substitution, or selected dangerous flags fall outside the allowlist. Approval elicitation failure or an unanswered request after five minutes is treated as denial.

**Safety: these controls are an application-layer policy for preventing accidental misuse. They do not defend against a malicious or adversarial model and do not provide OS-level isolation. Reads performed through `read_file` or allowlisted shell commands are not confined to `cwd` at any sandbox level; they can access any path the server process can read. Shell-internal writes that escape the declared sandbox boundary cannot always be detected by this layer; in particular, `workspace-write` considers shell calls inside its boundary. Use operating-system isolation when the trust boundary requires it.**

## Events

During a running call, the server emits `deepseek/event` notifications with `threadId` and a `msg` object. The possible `msg.type` values are:

| Type | Meaning and fields |
|---|---|
| `task_started` | Processing began. |
| `agent_message_delta` | Streamed assistant text in `delta`. |
| `token_count` | Model-turn usage in `prompt_tokens`, `completion_tokens`, and `total_tokens`. |
| `exec_command_begin` | Tool execution began; includes `call_id`, `tool`, and `command` for shell or `path` for a file tool. |
| `exec_command_end` | Tool execution ended; includes `call_id`, `tool`, `exit_code` for shell, and `error` when execution failed. |
| `agent_message` | Final assistant text in `message`. |
| `task_complete` | The call completed successfully. |
| `error` | The call failed; `message` contains the error. |

Operations stopped by policy or denied approval do not begin execution and therefore do not emit `exec_command_begin` or `exec_command_end`.

## Notes

- This repository's `.mcp.json` exposes `ds-mcp` as a project-level MCP server when Claude Code is opened inside the repository, which is useful for self-testing.
- Sessions are stored in memory only. They are lost when the server restarts; there is no persistence or cross-process resume.
- Run `go test ./... -race` to execute the test suite with the race detector.

## Non-goals

The project deliberately does not provide OS-level sandboxing, persistent or restart-resumable sessions, an `apply_patch`-style tool, MCP resources or prompts, image input, or web search. The Claude Code plugin also deliberately omits agents, hooks, and background broker scripts.
