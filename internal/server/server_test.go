package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Geek0x0/ds-mcp/internal/agent"
	"github.com/Geek0x0/ds-mcp/internal/deepseek"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	openai "github.com/sashabaranov/go-openai"
)

type stubTurn struct {
	result *deepseek.TurnResult
	err    error
}

type stubChatClient struct {
	mu       sync.Mutex
	turns    []stubTurn
	repeat   *deepseek.TurnResult
	requests []openai.ChatCompletionRequest
	block    <-chan struct{}
	entered  chan<- struct{}
}

func (c *stubChatClient) ChatTurn(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	_ func(string),
) (*deepseek.TurnResult, error) {
	c.mu.Lock()
	requestCopy := req
	requestCopy.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages...)
	requestCopy.Tools = append([]openai.Tool(nil), req.Tools...)
	c.requests = append(c.requests, requestCopy)

	var turn stubTurn
	switch {
	case len(c.turns) > 0:
		turn = c.turns[0]
		c.turns = c.turns[1:]
	case c.repeat != nil:
		turn.result = c.repeat
	default:
		turn.err = errors.New("stub client has no queued turn")
	}
	block := c.block
	entered := c.entered
	c.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return turn.result, turn.err
}

func (c *stubChatClient) recordedRequests() []openai.ChatCompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]openai.ChatCompletionRequest(nil), c.requests...)
}

type emitterFunc func(context.Context, string, map[string]any)

func (f emitterFunc) Emit(ctx context.Context, threadID string, msg map[string]any) {
	f(ctx, threadID, msg)
}

type fakeElicitationSession struct {
	result        *mcp.ElicitationResult
	err           error
	requests      []mcp.ElicitationRequest
	notifications chan mcp.JSONRPCNotification
}

func (s *fakeElicitationSession) Initialize() {}

func (s *fakeElicitationSession) Initialized() bool {
	return true
}

func (s *fakeElicitationSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return s.notifications
}

func (s *fakeElicitationSession) SessionID() string {
	return "test-session"
}

func (s *fakeElicitationSession) RequestElicitation(
	_ context.Context,
	request mcp.ElicitationRequest,
) (*mcp.ElicitationResult, error) {
	s.requests = append(s.requests, request)
	return s.result, s.err
}

func TestToolDeclarations(t *testing.T) {
	tests := []struct {
		name       string
		tool       mcp.Tool
		properties []string
		required   []string
	}{
		{
			name: "deepseek",
			tool: deepseekTool(),
			properties: []string{
				"approval-policy",
				"base-instructions",
				"config",
				"cwd",
				"developer-instructions",
				"model",
				"prompt",
				"reasoning-effort",
				"sandbox",
			},
			required: []string{"prompt"},
		},
		{
			name:       "deepseek-reply",
			tool:       replyTool(),
			properties: []string{"prompt", "threadId"},
			required:   []string{"prompt", "threadId"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.tool.Name != test.name {
				t.Fatalf("tool name = %q, want %q", test.tool.Name, test.name)
			}

			gotProperties := make([]string, 0, len(test.tool.InputSchema.Properties))
			for name, rawProperty := range test.tool.InputSchema.Properties {
				gotProperties = append(gotProperties, name)
				property, ok := rawProperty.(map[string]any)
				if !ok {
					t.Fatalf("property %q = %#v, want map[string]any", name, rawProperty)
				}
				if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
					t.Errorf("property %q has no description", name)
				}
			}
			sort.Strings(gotProperties)
			if !reflect.DeepEqual(gotProperties, test.properties) {
				t.Fatalf("properties = %v, want %v", gotProperties, test.properties)
			}

			gotRequired := append([]string(nil), test.tool.InputSchema.Required...)
			sort.Strings(gotRequired)
			if !reflect.DeepEqual(gotRequired, test.required) {
				t.Fatalf("required = %v, want %v", gotRequired, test.required)
			}
		})
	}
}

func TestToolOutputSchemas(t *testing.T) {
	tests := []struct {
		name string
		tool mcp.Tool
	}{
		{name: "deepseek", tool: deepseekTool()},
		{name: "deepseek-reply", tool: replyTool()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.tool.OutputSchema.Type == "" {
				t.Fatalf("tool %q has no output schema", test.name)
			}
			raw, err := json.Marshal(test.tool.OutputSchema)
			if err != nil {
				t.Fatalf("marshal output schema: %v", err)
			}
			t.Logf("output schema: %s", raw)

			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("unmarshal output schema: %v", err)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("output schema properties = %#v, want map", schema["properties"])
			}
			for _, want := range []string{"threadId", "content"} {
				if _, ok := properties[want]; !ok {
					t.Errorf("output schema properties = %#v, want property %q", properties, want)
				}
			}
		})
	}
}

func TestHandleDeepseekValidation(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		name     string
		args     map[string]any
		contains []string
	}{
		{
			name:     "missing prompt",
			args:     map[string]any{},
			contains: []string{"prompt is required"},
		},
		{
			name:     "invalid sandbox",
			args:     map[string]any{"prompt": "hello", "sandbox": "bogus"},
			contains: []string{"bogus", "read-only", "workspace-write", "danger-full-access"},
		},
		{
			name:     "invalid approval policy",
			args:     map[string]any{"prompt": "hello", "approval-policy": "bogus"},
			contains: []string{"bogus", "untrusted", "on-request", "on-failure", "never"},
		},
		{
			name:     "invalid reasoning effort",
			args:     map[string]any{"prompt": "hello", "reasoning-effort": "bogus"},
			contains: []string{"bogus", "low", "high", "max"},
		},
		{
			name:     "relative cwd",
			args:     map[string]any{"prompt": "hello", "cwd": "relative/path"},
			contains: []string{"cwd", "absolute"},
		},
		{
			name:     "non-string cwd",
			args:     map[string]any{"prompt": "hello", "cwd": 123},
			contains: []string{"cwd", "string"},
		},
		{
			name:     "nonexistent cwd",
			args:     map[string]any{"prompt": "hello", "cwd": nonexistent},
			contains: []string{"cwd", "does not exist"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := New(&stubChatClient{}, "test")
			result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", test.args))
			if err != nil {
				t.Fatalf("handleDeepseek() Go error = %v, want nil", err)
			}
			if !result.IsError {
				t.Fatalf("handleDeepseek() result = %#v, want tool error", result)
			}
			text := toolResultText(t, result)
			for _, want := range test.contains {
				if !strings.Contains(text, want) {
					t.Errorf("result text = %q, want it to contain %q", text, want)
				}
			}
		})
	}
}

func TestHandleDeepseekAndReplyContinueSession(t *testing.T) {
	client := &stubChatClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{Content: "hi"}},
		{result: &deepseek.TurnResult{Content: "continued"}},
	}}
	s := New(client, "test")

	first, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt": "hello",
		"cwd":    t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("handleDeepseek() Go error = %v, want nil", err)
	}
	if first.IsError {
		t.Fatalf("handleDeepseek() result = %#v, want success", first)
	}
	if got := toolResultText(t, first); got != "hi" {
		t.Fatalf("handleDeepseek() text = %q, want %q", got, "hi")
	}
	if got := toolResultContent(t, first); got != "hi" {
		t.Fatalf("handleDeepseek() message = %q, want %q", got, "hi")
	}
	threadID := toolResultThreadID(t, first)

	reply, err := s.handleReply(context.Background(), callToolRequest("deepseek-reply", map[string]any{
		"threadId": threadID,
		"prompt":   "keep going",
	}))
	if err != nil {
		t.Fatalf("handleReply() Go error = %v, want nil", err)
	}
	if reply.IsError {
		t.Fatalf("handleReply() result = %#v, want success", reply)
	}
	if got := toolResultText(t, reply); got != "continued" {
		t.Fatalf("handleReply() text = %q, want %q", got, "continued")
	}
	if got := toolResultContent(t, reply); got != "continued" {
		t.Fatalf("handleReply() message = %q, want %q", got, "continued")
	}
	if got := toolResultThreadID(t, reply); got != threadID {
		t.Fatalf("handleReply() threadId = %q, want %q", got, threadID)
	}
}

func TestHandleDeepseekAppliesModelAndInstructions(t *testing.T) {
	client := &stubChatClient{turns: []stubTurn{{result: &deepseek.TurnResult{Content: "ok"}}}}
	s := New(client, "test")

	result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt":                 "hello",
		"cwd":                    t.TempDir(),
		"model":                  "deepseek-reasoner",
		"reasoning-effort":       "max",
		"base-instructions":      "custom base",
		"developer-instructions": "custom developer",
		"config": map[string]any{
			"unknown_future_option": true,
		},
	}))
	if err != nil || result.IsError {
		t.Fatalf("handleDeepseek() = (%#v, %v), want success", result, err)
	}

	requests := client.recordedRequests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if requests[0].Model != "deepseek-reasoner" {
		t.Fatalf("model = %q, want %q", requests[0].Model, "deepseek-reasoner")
	}
	if requests[0].ReasoningEffort != "max" {
		t.Fatalf("reasoning effort = %q, want %q", requests[0].ReasoningEffort, "max")
	}
	if got := requests[0].Messages[0].Content; got != "custom base\n\ncustom developer" {
		t.Fatalf("system prompt = %q, want custom base and developer instructions", got)
	}
}

func TestHandleDeepseekDefaultsReasoningEffort(t *testing.T) {
	client := &stubChatClient{turns: []stubTurn{{result: &deepseek.TurnResult{Content: "ok"}}}}
	s := New(client, "test")

	result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt": "hello",
		"cwd":    t.TempDir(),
	}))
	if err != nil || result.IsError {
		t.Fatalf("handleDeepseek() = (%#v, %v), want success", result, err)
	}

	requests := client.recordedRequests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if requests[0].ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want %q", requests[0].ReasoningEffort, "high")
	}
}

func TestHandleReplyUnknownThread(t *testing.T) {
	s := New(&stubChatClient{}, "test")
	result, err := s.handleReply(context.Background(), callToolRequest("deepseek-reply", map[string]any{
		"threadId": "not-a-thread",
		"prompt":   "hello",
	}))
	if err != nil {
		t.Fatalf("handleReply() Go error = %v, want nil", err)
	}
	if !result.IsError || !strings.Contains(toolResultText(t, result), "unknown threadId") {
		t.Fatalf("handleReply() result = %#v, want unknown threadId tool error", result)
	}
}

func TestHandleReplyBusy(t *testing.T) {
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	unblockClient := func() { unblockOnce.Do(func() { close(unblock) }) }
	t.Cleanup(unblockClient)

	entered := make(chan struct{}, 1)
	client := &stubChatClient{
		turns:   []stubTurn{{result: &deepseek.TurnResult{Content: "first done"}}},
		block:   unblock,
		entered: entered,
	}
	s := New(client, "test")
	cwd := t.TempDir()
	threadIDs := make(chan string, 1)
	s.runner.Emitter = emitterFunc(func(_ context.Context, threadID string, _ map[string]any) {
		select {
		case threadIDs <- threadID:
		default:
		}
	})

	firstResult := make(chan *mcp.CallToolResult, 1)
	firstError := make(chan error, 1)
	go func() {
		result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
			"prompt": "first",
			"cwd":    cwd,
		}))
		firstResult <- result
		firstError <- err
	}()

	var threadID string
	select {
	case threadID = <-threadIDs:
	case <-time.After(2 * time.Second):
		t.Fatal("first handleDeepseek() did not emit its threadId")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first handleDeepseek() did not reach the blocked client")
	}

	busy, err := s.handleReply(context.Background(), callToolRequest("deepseek-reply", map[string]any{
		"threadId": threadID,
		"prompt":   "second",
	}))
	if err != nil {
		t.Fatalf("handleReply() Go error = %v, want nil", err)
	}
	busyText := toolResultText(t, busy)
	if !busy.IsError || !strings.Contains(busyText, "busy") {
		t.Fatalf("handleReply() result = %#v, want busy tool error", busy)
	}
	if got := toolResultThreadID(t, busy); got != threadID {
		t.Fatalf("busy result threadId = %q, want %q", got, threadID)
	}
	if got := toolResultContent(t, busy); got != busyText {
		t.Fatalf("busy result message = %q, want it to equal text %q", got, busyText)
	}

	unblockClient()
	select {
	case result := <-firstResult:
		if result == nil || result.IsError || toolResultText(t, result) != "first done" {
			t.Fatalf("first handleDeepseek() result = %#v, want success", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first handleDeepseek() did not finish after unblocking the client")
	}
	if err := <-firstError; err != nil {
		t.Fatalf("first handleDeepseek() Go error = %v, want nil", err)
	}
}

func TestHandleDeepseekTurnLimitPreservesThreadID(t *testing.T) {
	client := &stubChatClient{repeat: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{
		toolCall("call-forever", "unknown_tool", `{}`),
	}}}
	s := New(client, "test")

	result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt": "never finish",
		"cwd":    t.TempDir(),
		"config": map[string]any{"max_turns": float64(1)},
	}))
	if err != nil {
		t.Fatalf("handleDeepseek() Go error = %v, want nil", err)
	}
	if !result.IsError {
		t.Fatalf("handleDeepseek() result = %#v, want turn-limit tool error", result)
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "turn limit reached (1)") {
		t.Fatalf("result text = %q, want turn limit", text)
	}
	if content := toolResultContent(t, result); content != text {
		t.Fatalf("result content = %q, want it to equal text %q", content, text)
	}
	if threadID := toolResultThreadID(t, result); threadID == "" {
		t.Fatal("turn-limit result has empty threadId")
	}
}

func TestHandleDeepseekIgnoresExcessiveMaxTurns(t *testing.T) {
	toolTurn := stubTurn{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{
		toolCall("call-forever", "unknown_tool", `{}`),
	}}}
	turns := make([]stubTurn, agent.DefaultMaxTurns+1)
	for i := 0; i < agent.DefaultMaxTurns; i++ {
		turns[i] = toolTurn
	}
	turns[agent.DefaultMaxTurns] = stubTurn{result: &deepseek.TurnResult{Content: "should not be reached"}}
	client := &stubChatClient{turns: turns}
	s := New(client, "test")

	result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt": "use a bounded turn count",
		"cwd":    t.TempDir(),
		"config": map[string]any{"max_turns": float64(100001)},
	}))
	if err != nil {
		t.Fatalf("handleDeepseek() Go error = %v, want nil", err)
	}
	if !result.IsError {
		t.Fatalf("handleDeepseek() result = %#v, want default turn-limit error", result)
	}
	want := fmt.Sprintf("turn limit reached (%d)", agent.DefaultMaxTurns)
	if text := toolResultText(t, result); !strings.Contains(text, want) {
		t.Fatalf("result text = %q, want it to contain %q", text, want)
	}
}

func TestApproveElicitationActions(t *testing.T) {
	tests := []struct {
		name   string
		action mcp.ElicitationResponseAction
		err    error
		want   bool
	}{
		{name: "accept", action: mcp.ElicitationResponseActionAccept, want: true},
		{name: "decline", action: mcp.ElicitationResponseActionDecline, want: false},
		{name: "cancel", action: mcp.ElicitationResponseActionCancel, want: false},
		{name: "error", err: errors.New("elicitation failed"), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := New(&stubChatClient{}, "test")
			var result *mcp.ElicitationResult
			if test.err == nil {
				result = &mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{Action: test.action},
				}
			}
			fakeSession := &fakeElicitationSession{
				result:        result,
				err:           test.err,
				notifications: make(chan mcp.JSONRPCNotification, 1),
			}
			ctx := s.mcp.WithContext(context.Background(), fakeSession)

			got := s.Approve(ctx, "thread-1", agent.ApprovalRequest{
				Tool:    "shell",
				Command: "echo hello",
				Reason:  "test approval",
			})
			if got != test.want {
				t.Fatalf("Approve() = %v, want %v", got, test.want)
			}
			if len(fakeSession.requests) != 1 {
				t.Fatalf("elicitation request count = %d, want 1", len(fakeSession.requests))
			}
			request := fakeSession.requests[0]
			for _, want := range []string{"thread-1", "shell", "echo hello", "test approval"} {
				if !strings.Contains(request.Params.Message, want) {
					t.Errorf("elicitation message = %q, want it to contain %q", request.Params.Message, want)
				}
			}
			if request.Params.RequestedSchema == nil {
				t.Fatal("elicitation request has nil RequestedSchema")
			}
		})
	}
}

func TestApprovalUnavailableDeniesToolCall(t *testing.T) {
	cwd := t.TempDir()
	client := &stubChatClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{
			toolCall("call-write", "write_file", `{"path":"blocked.txt","content":"nope"}`),
		}}},
		{result: &deepseek.TurnResult{Content: "denied safely"}},
	}}
	s := New(client, "test")

	result, err := s.handleDeepseek(context.Background(), callToolRequest("deepseek", map[string]any{
		"prompt":          "write a file",
		"cwd":             cwd,
		"sandbox":         "read-only",
		"approval-policy": "on-request",
	}))
	if err != nil || result.IsError {
		t.Fatalf("handleDeepseek() = (%#v, %v), want successful denial recovery", result, err)
	}
	if got := toolResultText(t, result); got != "denied safely" {
		t.Fatalf("result text = %q, want %q", got, "denied safely")
	}

	requests := client.recordedRequests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	toolOutput := toolMessageContent(t, requests[1].Messages, "call-write")
	if !strings.Contains(toolOutput, "approval was not granted") {
		t.Fatalf("tool output = %q, want approval denial", toolOutput)
	}
	if _, err := os.Stat(filepath.Join(cwd, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write created a file; Stat() error = %v", err)
	}
}

func TestEmitWithoutClientReturnsPromptly(t *testing.T) {
	s := New(&stubChatClient{}, "test")
	done := make(chan struct{})
	go func() {
		s.Emit(context.Background(), "thread", map[string]any{"type": "test"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit() blocked without an active MCP client")
	}
}

func callToolRequest(name string, arguments map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Request: mcp.Request{},
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func toolCall(id, name, arguments string) openai.ToolCall {
	return openai.ToolCall{
		ID:   id,
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("tool result is nil")
	}
	if len(result.Content) != 1 {
		t.Fatalf("tool result content count = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool result content = %#v, want mcp.TextContent", result.Content[0])
	}
	return text.Text
}

func toolResultThreadID(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want map[string]any", result.StructuredContent)
	}
	threadID, ok := structured["threadId"].(string)
	if !ok || threadID == "" {
		t.Fatalf("structured threadId = %#v, want non-empty string", structured["threadId"])
	}
	return threadID
}

func toolResultContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want map[string]any", result.StructuredContent)
	}
	content, ok := structured["content"].(string)
	if !ok {
		t.Fatalf("structured content = %#v, want string", structured["content"])
	}
	return content
}

func toolMessageContent(t *testing.T, messages []openai.ChatCompletionMessage, callID string) string {
	t.Helper()
	for _, message := range messages {
		if message.Role == openai.ChatMessageRoleTool && message.ToolCallID == callID {
			return message.Content
		}
	}
	t.Fatalf("no tool message found for call %q", callID)
	return ""
}

var _ agent.ChatClient = (*stubChatClient)(nil)
var _ mcpserver.SessionWithElicitation = (*fakeElicitationSession)(nil)
