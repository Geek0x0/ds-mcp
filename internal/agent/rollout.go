package agent

import (
	"encoding/json"
	"time"

	"github.com/Geek0x0/subagent-mcp/internal/patch"
	"github.com/Geek0x0/subagent-mcp/internal/provider/chatcompletions"

	openai "github.com/sashabaranov/go-openai"
)

type tokenUsage struct {
	Input, Cached, Output, Reasoning, Total int
}

func usageFrom(u *openai.Usage) tokenUsage {
	t := tokenUsage{Input: u.PromptTokens, Output: u.CompletionTokens, Total: u.TotalTokens}
	if u.PromptTokensDetails != nil {
		t.Cached = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		t.Reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	return t
}

func (t tokenUsage) add(o tokenUsage) tokenUsage {
	return tokenUsage{t.Input + o.Input, t.Cached + o.Cached, t.Output + o.Output, t.Reasoning + o.Reasoning, t.Total + o.Total}
}

func (t tokenUsage) payload() map[string]any {
	return map[string]any{
		"input_tokens":            t.Input,
		"cached_input_tokens":     t.Cached,
		"output_tokens":           t.Output,
		"reasoning_output_tokens": t.Reasoning,
		"total_tokens":            t.Total,
	}
}

func textContent(kind, text string) []any {
	return []any{map[string]any{"type": kind, "text": text}}
}

func (s *Session) sandboxPolicy() map[string]any {
	roots, _ := s.shellWritableRoots()
	return map[string]any{"type": string(s.sandbox), "writable_roots": roots}
}

func recordTurnStart(s *Session, prompt string, started time.Time) {
	s.rollout.Write("turn_context", map[string]any{
		"turn_id":         s.turnID,
		"cwd":             s.cwd,
		"approval_policy": string(s.approval),
		"sandbox_policy":  s.sandboxPolicy(),
		"model":           s.model,
		"effort":          s.requestedEffort,
		"current_date":    started.Format("2006-01-02"),
	})
	s.rollout.Event(map[string]any{"type": "task_started", "turn_id": s.turnID, "started_at": started.Unix()})
	s.rollout.Item(map[string]any{"type": "message", "role": "user", "content": textContent("input_text", prompt)})
	s.rollout.Event(map[string]any{"type": "user_message", "message": prompt})
}

func recordModelTurn(s *Session, res *chatcompletions.TurnResult) {
	if res.Reasoning != "" {
		s.rollout.Item(map[string]any{"type": "reasoning", "summary": []any{}, "content": textContent("reasoning_text", res.Reasoning)})
	}
	if res.Usage != nil {
		last := usageFrom(res.Usage)
		s.totalUsage = s.totalUsage.add(last)
		s.rollout.Event(map[string]any{"type": "token_count", "info": map[string]any{
			"total_token_usage": s.totalUsage.payload(),
			"last_token_usage":  last.payload(),
		}})
	}
	if res.Content != "" {
		s.rollout.Item(map[string]any{"type": "message", "role": "assistant", "content": textContent("output_text", res.Content)})
		s.rollout.Event(map[string]any{"type": "agent_message", "message": res.Content})
	}
}

func recordToolCall(s *Session, call openai.ToolCall) {
	if call.Function.Name == "apply_patch" {
		var args struct {
			Patch string `json:"patch"`
		}
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		s.rollout.Item(map[string]any{"type": "custom_tool_call", "status": "completed", "call_id": call.ID, "name": "apply_patch", "input": args.Patch})
		return
	}
	s.rollout.Item(map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
}

func recordToolOutput(s *Session, call openai.ToolCall, output string) {
	itemType := "function_call_output"
	if call.Function.Name == "apply_patch" {
		itemType = "custom_tool_call_output"
	}
	s.rollout.Item(map[string]any{"type": itemType, "call_id": call.ID, "output": output})
}

func recordPatchApplied(s *Session, callID string, changes []patch.Change, output string, err error) {
	payload := map[string]any{"type": "patch_apply_end", "call_id": callID, "turn_id": s.turnID, "success": err == nil, "stdout": "", "stderr": ""}
	if err != nil {
		payload["stderr"] = output
	} else {
		payload["stdout"] = output
	}
	byPath := map[string]any{}
	for _, change := range changes {
		entry := map[string]any{"type": change.Kind.String(), "move_path": nil}
		switch change.Kind {
		case patch.Add:
			entry["content"] = change.NewContent
		case patch.Update:
			entry["unified_diff"] = change.Diff
			if change.MoveTo != "" {
				entry["move_path"] = change.MoveTo
			}
		}
		byPath[change.Path] = entry
	}
	payload["changes"] = byPath
	s.rollout.Event(payload)
}

func recordTaskComplete(s *Session, message string, started time.Time) {
	completed := time.Now()
	s.rollout.Event(map[string]any{
		"type":               "task_complete",
		"turn_id":            s.turnID,
		"last_agent_message": message,
		"started_at":         started.Unix(),
		"completed_at":       completed.Unix(),
		"duration_ms":        completed.Sub(started).Milliseconds(),
	})
}

func recordError(s *Session, err error) {
	s.rollout.Event(map[string]any{"type": "error", "message": err.Error()})
}
