package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Geek0x0/ds-mcp/internal/deepseek"
	"github.com/Geek0x0/ds-mcp/internal/patch"
	"github.com/Geek0x0/ds-mcp/internal/policy"
	"github.com/Geek0x0/ds-mcp/internal/tools"

	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"
)

var ErrBusy = errors.New("thread is busy")

type ChatClient interface {
	ChatTurn(
		ctx context.Context,
		req openai.ChatCompletionRequest,
		onDelta func(string),
	) (*deepseek.TurnResult, error)
}

type Emitter interface {
	Emit(ctx context.Context, threadID string, msg map[string]any)
}

type ApprovalRequest struct {
	Tool    string
	Command string
	Path    string
	Reason  string
}

type Approver interface {
	Approve(ctx context.Context, threadID string, req ApprovalRequest) bool
}

type Runner struct {
	Client   ChatClient
	Emitter  Emitter
	Approver Approver
}

func builtinTools() []openai.Tool {
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "shell",
				Description: "Run a bash command in the working directory. The sandbox policy may deny the call; " +
					"providing justification helps if approval is required.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"command": {"type": "string", "description": "Bash command to run."},
						"timeout_seconds": {"type": "integer", "description": "Optional timeout in seconds (clamped, max 600)."},
						"justification": {"type": "string", "description": "Why this call is needed if approval is required."}
					},
					"required": ["command"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "read_file",
				Description: "Read a file, resolving relative paths against the working directory. The sandbox policy may deny the call; " +
					"providing justification helps if approval is required.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "File path to read."},
						"justification": {"type": "string", "description": "Why this call is needed if approval is required."}
					},
					"required": ["path"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "write_file",
				Description: "Create or overwrite a whole file, creating parent directories as needed. The sandbox policy may deny the call; " +
					"providing justification helps if approval is required.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "File path to write."},
						"content": {"type": "string", "description": "Complete file content."},
						"justification": {"type": "string", "description": "Why this call is needed if approval is required."}
					},
					"required": ["path", "content"]
				}`),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "apply_patch",
				Description: "Edit files with a patch: '*** Begin Patch', then '*** Add File: <path>' (+lines), " +
					"'*** Delete File: <path>', or '*** Update File: <path>' (optional '*** Move to: <path>') with '@@' chunks " +
					"of ' ' context, '-' removed, and '+' added lines, then '*** End Patch'. Prefer this for editing existing files. " +
					"The sandbox policy may deny the call; providing justification helps if approval is required.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"patch": {"type": "string", "description": "Complete patch text from *** Begin Patch to *** End Patch."},
						"justification": {"type": "string", "description": "Why this call is needed if approval is required."}
					},
					"required": ["patch"]
				}`),
			},
		},
	}
}

func (r *Runner) Run(ctx context.Context, s *Session, prompt string) (string, error) {
	// ponytail: This lock spans model, approval, tool, and emitter calls, so a hung dependency leaves the thread busy;
	// per-phase state locking plus a caller-visible cancel/abort channel would remove that ceiling.
	if !s.mu.TryLock() {
		return "", ErrBusy
	}
	defer s.mu.Unlock()

	r.Emitter.Emit(ctx, s.ID, map[string]any{"type": "task_started"})
	started := time.Now()
	s.turnID = uuid.NewString()
	recordTurnStart(s, prompt, started)
	s.messages = append(s.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: prompt,
	})

	for turn := 0; turn < s.maxTurns; turn++ {
		res, err := r.Client.ChatTurn(
			ctx,
			openai.ChatCompletionRequest{
				Model:           s.model,
				Messages:        s.messages,
				Tools:           builtinTools(),
				ReasoningEffort: s.reasoningEffort,
			},
			func(delta string) {
				r.Emitter.Emit(ctx, s.ID, map[string]any{
					"type":  "agent_message_delta",
					"delta": delta,
				})
			},
		)
		if err != nil {
			r.Emitter.Emit(ctx, s.ID, map[string]any{
				"type":    "error",
				"message": err.Error(),
			})
			recordError(s, err)
			return "", err
		}
		if res.Usage != nil {
			r.Emitter.Emit(ctx, s.ID, map[string]any{
				"type":              "token_count",
				"prompt_tokens":     res.Usage.PromptTokens,
				"completion_tokens": res.Usage.CompletionTokens,
				"total_tokens":      res.Usage.TotalTokens,
			})
		}
		recordModelTurn(s, res)

		s.messages = append(s.messages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			Content:   res.Content,
			ToolCalls: res.ToolCalls,
		})
		if len(res.ToolCalls) == 0 {
			r.Emitter.Emit(ctx, s.ID, map[string]any{
				"type":    "agent_message",
				"message": res.Content,
			})
			r.Emitter.Emit(ctx, s.ID, map[string]any{"type": "task_complete"})
			recordTaskComplete(s, res.Content, started)
			return res.Content, nil
		}

		for _, toolCall := range res.ToolCalls {
			recordToolCall(s, toolCall)
			content := r.safeExecToolCall(ctx, s, toolCall)
			if content == "" {
				content = "(empty output)"
			}
			recordToolOutput(s, toolCall, content)
			s.messages = append(s.messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: toolCall.ID,
				Content:    content,
			})
		}
	}

	err := fmt.Errorf("turn limit reached (%d) without a final answer", s.maxTurns)
	r.Emitter.Emit(ctx, s.ID, map[string]any{
		"type":    "error",
		"message": err.Error(),
	})
	recordError(s, err)
	return "", err
}

func (r *Runner) safeExecToolCall(ctx context.Context, s *Session, toolCall openai.ToolCall) (result string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = fmt.Sprintf("internal error: tool execution panicked: %v", recovered)
		}
	}()

	return r.execToolCall(ctx, s, toolCall)
}

func (r *Runner) execToolCall(ctx context.Context, s *Session, toolCall openai.ToolCall) string {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Path           string `json:"path"`
		Content        string `json:"content"`
		Patch          string `json:"patch"`
		Justification  string `json:"justification"`
	}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "invalid tool arguments: " + err.Error()
	}

	switch toolCall.Function.Name {
	case "shell", "read_file", "write_file", "apply_patch":
	default:
		return "unknown tool: " + toolCall.Function.Name
	}

	requests := []policy.Request{{Tool: toolCall.Function.Name, Command: args.Command, Path: args.Path, Cwd: s.cwd}}
	var hunks []patch.Hunk
	var paths []string
	if toolCall.Function.Name == "apply_patch" {
		parsed, err := patch.Parse(args.Patch)
		if err != nil {
			return "error: invalid patch: " + err.Error()
		}
		hunks = parsed
		paths = patch.Paths(hunks)
		requests = requests[:0]
		for _, path := range paths {
			requests = append(requests, policy.Request{Tool: "write_file", Path: path, Cwd: s.cwd})
		}
	}
	approved, denial := r.authorize(ctx, s, toolCall.Function.Name, args.Command, args.Justification, requests)
	if denial != "" {
		return denial
	}

	beginEvent := map[string]any{
		"type":    "exec_command_begin",
		"call_id": toolCall.ID,
		"tool":    toolCall.Function.Name,
	}
	switch toolCall.Function.Name {
	case "shell":
		beginEvent["command"] = args.Command
	case "apply_patch":
		beginEvent["paths"] = paths
	default:
		beginEvent["path"] = args.Path
	}

	var exitCode *int
	if toolCall.Function.Name == "shell" {
		unknownExitCode := -1
		exitCode = &unknownExitCode
	}
	var toolErr error
	defer func() {
		r.emitExecEnd(ctx, s, toolCall.ID, toolCall.Function.Name, exitCode, toolErr, paths)
	}()
	r.Emitter.Emit(ctx, s.ID, beginEvent)

	switch toolCall.Function.Name {
	case "shell":
		timeout := time.Duration(args.TimeoutSeconds) * time.Second
		var out string
		var code int
		var err error
		if roots, sandboxed := s.shellWritableRoots(); sandboxed && !approved {
			out, code, err = tools.RunShellSandboxed(ctx, s.cwd, args.Command, timeout, roots)
		} else {
			// ponytail: A human-approved command runs without the kernel sandbox, matching Codex escalation.
			out, code, err = tools.RunShell(ctx, s.cwd, args.Command, timeout)
		}
		*exitCode = code
		toolErr = err

		result := fmt.Sprintf("exit code: %d\n%s", code, out)
		if err != nil {
			result += fmt.Sprintf("\n[error: %s]", err)
		}
		return result

	case "read_file":
		content, err := tools.ReadFile(ctx, s.cwd, args.Path)
		toolErr = err

		if err != nil {
			return "error: " + err.Error()
		}
		return content

	case "write_file":
		err := tools.WriteFile(s.cwd, args.Path, args.Content)
		toolErr = err

		if err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)

	case "apply_patch":
		changes, err := patch.Plan(s.cwd, hunks)
		if err == nil {
			err = patch.Commit(changes)
		}
		toolErr = err
		if err != nil {
			result := "error: " + err.Error()
			recordPatchApplied(s, toolCall.ID, nil, result, err)
			return result
		}
		result := patch.Summary(changes)
		recordPatchApplied(s, toolCall.ID, changes, result, nil)
		return result
	}

	return "unknown tool: " + toolCall.Function.Name
}

// authorize evaluates every request; any Deny rejects the call, and all
// approval-requiring requests are combined into one approval prompt.
func (r *Runner) authorize(
	ctx context.Context,
	s *Session,
	tool, command, justification string,
	requests []policy.Request,
) (approved bool, denial string) {
	var reasons, paths []string
	for _, req := range requests {
		decision, reason := policy.Evaluate(s.sandbox, s.approval, req)
		switch decision {
		case policy.Deny:
			return false, "operation denied by sandbox policy: " + reason
		case policy.AskApproval:
			reasons = append(reasons, reason)
			if req.Path != "" {
				paths = append(paths, req.Path)
			}
		}
	}
	if len(reasons) == 0 {
		return false, ""
	}
	approvalReason := strings.Join(reasons, "; ")
	if justification != "" {
		approvalReason = fmt.Sprintf("%s (model justification: %s)", approvalReason, justification)
	}
	if !r.Approver.Approve(ctx, s.ID, ApprovalRequest{
		Tool:    tool,
		Command: command,
		Path:    strings.Join(paths, ", "),
		Reason:  approvalReason,
	}) {
		return false, "operation denied: approval was not granted"
	}
	return true, ""
}

func (r *Runner) emitExecEnd(
	ctx context.Context,
	s *Session,
	callID string,
	tool string,
	exitCode *int,
	err error,
	paths []string,
) {
	endEvent := map[string]any{
		"type":    "exec_command_end",
		"call_id": callID,
		"tool":    tool,
	}
	if exitCode != nil {
		endEvent["exit_code"] = *exitCode
	}
	if paths != nil {
		endEvent["paths"] = paths
	}
	if err != nil {
		endEvent["error"] = err.Error()
	}
	r.Emitter.Emit(ctx, s.ID, endEvent)
}
