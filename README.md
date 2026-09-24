# ds-mcp

ds-mcp exposes DeepSeek as a full MCP coding agent with real shell and file-tool execution, rather than acting as a thin passthrough to the DeepSeek chat API.

## Install

1. Install the binary directly from GitHub:

   ```bash
   go install github.com/Geek0x0/ds-mcp/cmd/ds-mcp@latest
   ```

   Alternatively, from a local clone of the ds-mcp repository, run:

   ```bash
   go install ./cmd/ds-mcp
   ```

   Make sure `$(go env GOPATH)/bin` is on `PATH`.

2. Add this repository as a Claude Code plugin marketplace:

   ```bash
   claude plugin marketplace add Geek0x0/ds-mcp
   ```

   Alternatively, if you already have the repository checked out locally, run:

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
| `reasoning-effort` | No | `high` | Reasoning effort: `low`, `medium`, `high`, `xhigh`, or `max`. `medium` is sent to the API as `high`, and `xhigh` as `max`. This argument wins over `config.model_reasoning_effort`; an invalid value is an error. |
| `cwd` | No | Server process working directory | Absolute path to an existing directory. |
| `sandbox` | No | `read-only` | `read-only`, `workspace-write`, or `danger-full-access`. |
| `approval-policy` | No | `on-request` | `untrusted`, `on-request`, `on-failure`, or `never`. |
| `base-instructions` | No | Built-in instructions | Complete replacement for the built-in base system instructions. An empty or omitted value uses the built-in default. |
| `developer-instructions` | No | None | Additional system instructions appended after the AGENTS.md blocks. An empty or omitted value appends nothing. |
| `config` | No | `{}` | Loose object. Recognized keys: `max_turns` (number from 1 to 100000, default 50, out-of-range values silently ignored), `model_reasoning_effort` (same values as `reasoning-effort`; the top-level argument wins), and `writable_roots` (array of absolute paths to existing directories that the shell may also write under `workspace-write`). Invalid `model_reasoning_effort` or `writable_roots` values are errors; other unknown keys are silently ignored. |

The response includes `structuredContent.threadId`. Retain it to continue the session. Once a session is created, execution errors also return its `threadId`, so the session remains resumable.

The system prompt is assembled as base instructions, then `AGENTS.md` blocks, then developer instructions. At session creation the server resolves `cwd` (after symlink evaluation), walks up to the nearest ancestor containing a `.git` entry, and collects `AGENTS.md` from that root down to `cwd`; without a repository root only `cwd` is read. Each file is wrapped in an `<agents_md path="...">` block, and the combined content is capped at 32 KiB with a truncation marker. A missing file is skipped, but any other read error fails the call.

### `deepseek-reply`

Continues an existing coding-agent thread. Session settings cannot be changed on a reply.

| Parameter | Required | Description |
|---|---:|---|
| `threadId` | Yes | String thread ID returned by an earlier `deepseek` or `deepseek-reply` call. |
| `prompt` | Yes | String follow-up prompt for the existing thread. |

Only one call can process a thread at a time. A concurrent reply returns a `busy` error with the same `threadId`. An unknown ID returns `unknown threadId`; check the ID or create a new session if the server has restarted.

### Agent tools

The DeepSeek agent runs inside the thread with four built-in tools:

| Tool | Arguments | Description |
|---|---|---|
| `shell` | `command`, optional `timeout_seconds`, optional `justification` | Run a bash command in `cwd`. Timeouts are clamped to 600 seconds. |
| `read_file` | `path`, optional `justification` | Read a file; relative paths resolve against `cwd`. |
| `write_file` | `path`, `content`, optional `justification` | Create or overwrite a whole file, creating parent directories. |
| `apply_patch` | `patch`, optional `justification` | Edit files with a Codex-format patch. |

`apply_patch` accepts a patch from `*** Begin Patch` to `*** End Patch` containing one or more `*** Add File: <path>` (with `+` content lines), `*** Delete File: <path>`, or `*** Update File: <path>` sections, an optional `*** Move to: <path>` after an update header, `@@` chunk headers, and `' '` context, `-` removed, and `+` added lines. `*** End of File` anchors a chunk at the end of the file. Leading and trailing whitespace around the whole patch, a `<<'EOF'` heredoc wrapper, and CRLF line endings are tolerated. The whole patch is parsed and matched in memory first: if any chunk fails to match, or an Add targets an existing file, nothing is written. Context matching tries exact lines, then ignores trailing whitespace, then ignores surrounding whitespace. The tool result starts with `Success. Updated the following files:` followed by `A`, `M`, or `D` plus the path of each change.

## Sandbox and approvals

The sandbox classifies operations as inside or outside its boundary. The approval policy then decides whether the operation runs, asks the MCP client for approval, or is denied.

| Sandbox | `untrusted` | `on-request` | `on-failure` | `never` |
|---|---|---|---|---|
| `read-only` | Allow `read_file` and allowlisted shell; ask for everything else | Allow `read_file` and allowlisted shell; ask for everything else | Same as `on-request` | Allow `read_file` and allowlisted shell; deny everything else |
| `workspace-write` | Allow `read_file` and allowlisted shell; ask for other shell and all `write_file` calls | Allow `read_file`, all shell, and `write_file` inside `cwd`; ask for `write_file` outside `cwd` | Same as `on-request` | Allow `read_file`, all shell, and `write_file` inside `cwd`; deny `write_file` outside `cwd` |
| `danger-full-access` | Allow `read_file` and allowlisted shell; ask for all other operations | Allow all operations | Same as `on-request` | Allow all operations |

The shell allowlist covers `ls`, `cat`, `head`, `tail`, `rg`, `grep`, `find`, `pwd`, `wc`, `stat`, `which`, and `echo`, plus the Git subcommands `status`, `diff`, `log`, `show`, `branch`, `blame`, `rev-parse`, and `ls-files`. Commands with output redirection, command substitution, process substitution, or selected dangerous flags fall outside the allowlist. Approval elicitation failure or an unanswered request after five minutes is treated as denial.

`apply_patch` is evaluated like `write_file` once per touched path (including `*** Move to:` targets). Any denied path denies the whole patch, so a single patch cannot write inside `cwd` and outside it at the same time; every path that needs approval is listed in one combined approval request, and that approval covers the whole patch.

### Kernel sandbox for shell calls

Shell calls that the policy auto-allows run under a Landlock ruleset: the server re-executes its own binary as `ds-mcp __sandbox-exec --rw <path>... -- bash -lc <command>`, which restricts itself to read and execute everywhere plus write access only beneath the listed roots, and then `exec`s the command. The restriction is inherited by every descendant process, so writes are blocked by the kernel rather than by command-string inspection.

| Sandbox | Writable roots for auto-allowed shell calls |
|---|---|
| `read-only` | None |
| `workspace-write` | `cwd`, `/tmp`, `$TMPDIR` when set and different and an existing directory, plus `config.writable_roots` |
| `danger-full-access` | Unrestricted; the command runs without the helper |

In both wrapped modes the device files `/dev/null`, `/dev/zero`, `/dev/full`, `/dev/random`, `/dev/urandom`, and `/dev/tty` are also writable; the rest of `/dev`, including `/dev/shm`, is read-only. On Landlock ABI v2+ kernels (Linux 5.19+), files can be renamed or hard-linked between directories inside the writable roots.

Shell calls that a human approved through elicitation run without the kernel sandbox, matching Codex escalation semantics. The sandbox fails closed: on a kernel without Landlock (Linux below 5.13) or on a non-Linux platform, a wrapped shell call exits with code 126 and `ds-mcp: landlock unavailable: ...` instead of running unsandboxed.

**Safety: the application-layer policy prevents accidental misuse, and the Landlock wrapper confines auto-allowed shell writes to the roots above. Reads and network access are still unrestricted, and `write_file` and `apply_patch` writes are limited to `cwd` by an in-process path check rather than by the kernel. On Landlock ABI v1 kernels (Linux 5.13–5.18), renaming or hard-linking a file into a different directory always fails with `EXDEV` (tools such as `git mv` break, while `mv` falls back to copying), and truncating existing files outside the writable roots is not restricted. Human-approved and `danger-full-access` shell calls are not sandboxed at all. Use stronger operating-system isolation when the trust boundary requires it.**

## Events

During a running call, the server emits `deepseek/event` notifications with `threadId` and a `msg` object. The possible `msg.type` values are:

| Type | Meaning and fields |
|---|---|
| `task_started` | Processing began. |
| `agent_message_delta` | Streamed assistant text in `delta`. |
| `token_count` | Model-turn usage in `prompt_tokens`, `completion_tokens`, and `total_tokens`. |
| `exec_command_begin` | Tool execution began; includes `call_id`, `tool`, and `command` for shell, `paths` (array) for `apply_patch`, or `path` for `read_file` and `write_file`. |
| `exec_command_end` | Tool execution ended; includes `call_id`, `tool`, `exit_code` for shell, `paths` for `apply_patch`, and `error` when execution failed. |
| `agent_message` | Final assistant text in `message`. |
| `task_complete` | The call completed successfully. |
| `error` | The call failed; `message` contains the error. |

Operations stopped by policy or denied approval do not begin execution and therefore do not emit `exec_command_begin` or `exec_command_end`.

## Rollout files

Every session writes a Codex-compatible rollout as JSONL under `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<YYYY-MM-DDTHH-MM-SS>-<threadId>.jsonl`, using UTC and `~/.codex` when `CODEX_HOME` is unset. Directories are created `0700` and the file is `0600`. Each line is `{"timestamp": ..., "type": ..., "payload": {...}}` with the Codex line types `session_meta`, `turn_context`, `response_item`, and `event_msg`. Codex ecosystem tools can read these files for usage reporting, session viewing, and audit; `originator` is `ds-mcp` and `model_provider` is `deepseek`, and `session_meta` records the working directory, the full composed system prompt, and the Git branch and commit when `cwd` is in a repository. Because the files are written but never read back, these sessions are visible to `codex resume` but cannot actually be resumed by Codex.

Set `DS_MCP_ROLLOUT=off` to disable rollout writing. A rollout write or open failure logs one line to stderr and disables the rollout for that session; it never fails an agent run.

The rollout records the prompt, the composed system prompt, tool arguments, tool results, and command output, so it can contain secrets and file contents. Treat the rollout directory with the same care as the session transcript.

## Notes

- This repository's `.mcp.json` exposes `ds-mcp` as a project-level MCP server when Claude Code is opened inside the repository, which is useful for self-testing.
- Sessions are stored in memory only. They are lost when the server restarts; there is no persistence or cross-process resume, and rollout files are write-only.
- Run `go test ./... -race` to execute the test suite with the race detector.

## Non-goals

The project deliberately does not provide network or read sandboxing, sandboxing on non-Linux platforms, persistent or restart-resumable sessions, MCP resources or prompts, image input, or web search. The Claude Code plugin also deliberately omits agents, hooks, and background broker scripts.
