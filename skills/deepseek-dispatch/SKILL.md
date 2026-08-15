---
name: deepseek-dispatch
description: Dispatch coding work units to the DeepSeek agent via the deepseek / deepseek-reply MCP tools. Use when delegating implementation work to DeepSeek or continuing an existing DeepSeek thread.
---

# DeepSeek Dispatch

## New session vs reply

Call `deepseek` for a new, independent work unit. Start a new session whenever the task needs a different working directory, model, sandbox, approval policy, instruction set, or turn limit. These settings are fixed when the session is created.

Call `deepseek-reply` for another step in the same task: answer a question from the agent, clarify or correct its work, provide test-failure output, or request another verification pass. A reply accepts only `threadId` and `prompt`; the original settings carry over unchanged and the accumulated conversation history is reused.

A `busy` error means another call is already processing that thread. Wait for that call to finish, then retry the reply; use a new session only if the work is genuinely independent. An `unknown threadId` error means the server has no session with that ID, commonly because the ID is wrong or the server restarted. Check the ID, or start a new `deepseek` session if the original session is no longer in memory.

## threadId

For every response associated with a newly created or existing session, read `structuredContent.threadId` and retain it. It is present on successful responses and on execution errors, including `busy`, so a session remains available for follow-up after an error. Argument-validation failures before a session is created and an `unknown threadId` reply cannot provide a resumable session ID.

Use the most recent returned ID with `deepseek-reply`; the ID remains the same for the life of the session. Sessions exist only in the server process's memory and do not survive a restart.

## Parameter selection

Choose the narrowest sandbox that permits the task:

- `read-only` (default) permits `read_file` and the policy's shell-command allowlist. Other operations fall outside the sandbox.
- `workspace-write` permits `read_file`, all `shell` calls, and `write_file` when its resolved path stays inside `cwd`. A `write_file` path containing `..` or escaping through a symlink is outside the sandbox.
- `danger-full-access` treats every built-in tool operation as inside the sandbox.

The shell-command allowlist contains `ls`, `cat`, `head`, `tail`, `rg`, `grep`, `find`, `pwd`, `wc`, `stat`, `which`, and `echo`, plus `git status`, `git diff`, `git log`, `git show`, `git branch`, `git blame`, `git rev-parse`, and `git ls-files`.

**Safety: these sandbox modes are application-layer policy intended to prevent accidental misuse. They are not malicious-actor-proof and do not provide OS-level isolation. Reads performed through `read_file` or allowlisted shell commands are not confined to `cwd` at any sandbox level; they can access any path the server process can read. This layer also cannot always detect writes performed internally by an allowed shell command that escape the declared sandbox boundary; in particular, `workspace-write` considers shell calls inside its boundary. Use operating-system isolation when the trust boundary requires it.**

The `approval-policy` determines what happens to operations inside or outside the selected sandbox:

- `untrusted` allows `read_file` and allowlisted shell commands, and asks for approval for every other operation.
- `on-request` (default) allows operations inside the sandbox and asks for approval outside it.
- `on-failure` currently has exactly the same behavior as `on-request`.
- `never` allows operations inside the sandbox and denies operations outside it without asking.

Approval requests are sent to the MCP client. If approval elicitation is unavailable or unanswered for five minutes, the request is denied.

`cwd` must be an absolute path to an existing directory; a Git worktree root is usually a sensible choice. If omitted, it defaults to the ds-mcp process working directory. `model` defaults to `deepseek-chat`; pass `deepseek-reasoner` when the reasoning model is appropriate.

The optional loose `config` object recognizes `max_turns`. A number from 1 through 100000 overrides the default limit of 50 agent turns. Unknown keys, values of the wrong type, and out-of-range values are silently ignored.

Use `base-instructions` only to replace the built-in base system instructions completely. Use `developer-instructions` to append additional system instructions after the selected base instructions.

## Event stream

While a call runs, the server sends `deepseek/event` notifications containing the outer fields `threadId` and `msg`. The `msg.type` values are:

- `task_started`: the call was accepted and processing began.
- `agent_message_delta`: streamed assistant text; `delta` contains the new text fragment.
- `token_count`: token usage reported for a model turn, with `prompt_tokens`, `completion_tokens`, and `total_tokens`.
- `exec_command_begin`: tool execution is starting; includes `call_id`, `tool`, and either `command` for `shell` or `path` for file tools.
- `exec_command_end`: tool execution finished; includes `call_id` and `tool`, plus `exit_code` for `shell` and `error` when execution failed.
- `agent_message`: the final assistant response, in `message`.
- `task_complete`: the thread call completed successfully.
- `error`: the call failed, with the failure text in `message`.

Policy denials and rejected approval requests are returned to the model as tool results; a tool that never begins execution does not emit the begin/end pair.

## Recommended prompt structure

Give each work unit an explicit goal, a bounded file scope, constraints, verification commands, and the report format you expect. Include enough context for the agent to finish without guessing, while keeping unrelated work out of scope.

Example:

```text
Goal: Add table-driven tests for ParseDuration covering valid units and malformed input.

Scope:
- You may edit internal/config/duration_test.go only.
- Read internal/config/duration.go as needed.

Constraints:
- Do not change production code, dependencies, or generated files.
- Preserve the repository's existing test style.

Verification:
- Run: go test ./internal/config
- Run: go test ./... -race

Report:
- Summarize the cases added.
- List changed files.
- Give each verification command and its result.
- If blocked, report the exact blocker without broadening scope.
```
