package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ds-mcp/internal/deepseek"
	"ds-mcp/internal/policy"
	"ds-mcp/internal/tools"

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
	}
}

func (r *Runner) Run(ctx context.Context, s *Session, prompt string) (string, error) {
	if !s.mu.TryLock() {
		return "", ErrBusy
	}
	defer s.mu.Unlock()

	r.Emitter.Emit(ctx, s.ID, map[string]any{"type": "task_started"})
	s.messages = append(s.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: prompt,
	})

	for turn := 0; turn < s.maxTurns; turn++ {
		res, err := r.Client.ChatTurn(
			ctx,
			openai.ChatCompletionRequest{
				Model:    s.model,
				Messages: s.messages,
				Tools:    builtinTools(),
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
			return res.Content, nil
		}

		for _, toolCall := range res.ToolCalls {
			s.messages = append(s.messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: toolCall.ID,
				Content:    r.execToolCall(ctx, s, toolCall),
			})
		}
	}

	err := fmt.Errorf("turn limit reached (%d) without a final answer", s.maxTurns)
	r.Emitter.Emit(ctx, s.ID, map[string]any{
		"type":    "error",
		"message": err.Error(),
	})
	return "", err
}

func (r *Runner) execToolCall(ctx context.Context, s *Session, toolCall openai.ToolCall) string {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Path           string `json:"path"`
		Content        string `json:"content"`
		Justification  string `json:"justification"`
	}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "invalid tool arguments: " + err.Error()
	}

	switch toolCall.Function.Name {
	case "shell", "read_file", "write_file":
	default:
		return "unknown tool: " + toolCall.Function.Name
	}

	decision, reason := policy.Evaluate(s.sandbox, s.approval, policy.Request{
		Tool:    toolCall.Function.Name,
		Command: args.Command,
		Path:    args.Path,
		Cwd:     s.cwd,
	})
	switch decision {
	case policy.Deny:
		return "operation denied by sandbox policy: " + reason
	case policy.AskApproval:
		if !r.Approver.Approve(ctx, s.ID, ApprovalRequest{
			Tool:    toolCall.Function.Name,
			Command: args.Command,
			Path:    args.Path,
			Reason:  args.Justification,
		}) {
			return "operation denied: approval was not granted"
		}
	}

	beginEvent := map[string]any{
		"type":    "exec_command_begin",
		"call_id": toolCall.ID,
		"tool":    toolCall.Function.Name,
	}
	if toolCall.Function.Name == "shell" {
		beginEvent["command"] = args.Command
	} else {
		beginEvent["path"] = args.Path
	}
	r.Emitter.Emit(ctx, s.ID, beginEvent)

	switch toolCall.Function.Name {
	case "shell":
		out, exitCode, err := tools.RunShell(
			ctx,
			s.cwd,
			args.Command,
			time.Duration(args.TimeoutSeconds)*time.Second,
		)
		endEvent := map[string]any{
			"type":      "exec_command_end",
			"call_id":   toolCall.ID,
			"tool":      toolCall.Function.Name,
			"exit_code": exitCode,
		}
		if err != nil {
			endEvent["error"] = err.Error()
		}
		r.Emitter.Emit(ctx, s.ID, endEvent)

		result := fmt.Sprintf("exit code: %d\n%s", exitCode, out)
		if err != nil {
			result += fmt.Sprintf("\n[error: %s]", err)
		}
		return result

	case "read_file":
		content, err := tools.ReadFile(s.cwd, args.Path)
		endEvent := map[string]any{
			"type":    "exec_command_end",
			"call_id": toolCall.ID,
			"tool":    toolCall.Function.Name,
		}
		if err != nil {
			endEvent["error"] = err.Error()
		}
		r.Emitter.Emit(ctx, s.ID, endEvent)

		if err != nil {
			return "error: " + err.Error()
		}
		return content

	case "write_file":
		err := tools.WriteFile(s.cwd, args.Path, args.Content)
		endEvent := map[string]any{
			"type":    "exec_command_end",
			"call_id": toolCall.ID,
			"tool":    toolCall.Function.Name,
		}
		if err != nil {
			endEvent["error"] = err.Error()
		}
		r.Emitter.Emit(ctx, s.ID, endEvent)

		if err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)
	}

	panic("unreachable")
}
