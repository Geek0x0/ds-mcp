package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Geek0x0/ds-mcp/internal/deepseek"
	"github.com/Geek0x0/ds-mcp/internal/policy"

	openai "github.com/sashabaranov/go-openai"
)

type stubTurn struct {
	result *deepseek.TurnResult
	err    error
	deltas []string
}

type stubClient struct {
	mu       sync.Mutex
	turns    []stubTurn
	requests []openai.ChatCompletionRequest
	block    <-chan struct{}
	entered  chan<- struct{}
}

func (c *stubClient) ChatTurn(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	onDelta func(string),
) (*deepseek.TurnResult, error) {
	c.mu.Lock()
	requestCopy := req
	requestCopy.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages...)
	requestCopy.Tools = append([]openai.Tool(nil), req.Tools...)
	c.requests = append(c.requests, requestCopy)
	if len(c.turns) == 0 {
		c.mu.Unlock()
		return nil, errors.New("stub client has no queued turn")
	}
	turn := c.turns[0]
	c.turns = c.turns[1:]
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
	for _, delta := range turn.deltas {
		onDelta(delta)
	}

	return turn.result, turn.err
}

func (c *stubClient) recordedRequests() []openai.ChatCompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]openai.ChatCompletionRequest(nil), c.requests...)
}

type recEmitter struct {
	mu     sync.Mutex
	events []map[string]any
}

type panicOnceEmitter struct {
	recorder  *recEmitter
	panicType string
	once      sync.Once
}

func (e *panicOnceEmitter) Emit(ctx context.Context, threadID string, msg map[string]any) {
	e.recorder.Emit(ctx, threadID, msg)
	if msg["type"] != e.panicType {
		return
	}

	shouldPanic := false
	e.once.Do(func() { shouldPanic = true })
	if shouldPanic {
		panic("emitter boom")
	}
}

func (e *recEmitter) Emit(_ context.Context, _ string, msg map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()

	copyOfMessage := make(map[string]any, len(msg))
	for key, value := range msg {
		copyOfMessage[key] = value
	}
	e.events = append(e.events, copyOfMessage)
}

func (e *recEmitter) recordedEvents() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()

	events := make([]map[string]any, len(e.events))
	for i, event := range e.events {
		events[i] = make(map[string]any, len(event))
		for key, value := range event {
			events[i][key] = value
		}
	}
	return events
}

type stubApprover struct {
	mu       sync.Mutex
	approved bool
	requests []ApprovalRequest
}

func (a *stubApprover) Approve(_ context.Context, _ string, req ApprovalRequest) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.requests = append(a.requests, req)
	return a.approved
}

func (a *stubApprover) recordedRequests() []ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]ApprovalRequest(nil), a.requests...)
}

func TestRunnerPureTextOneTurn(t *testing.T) {
	client := &stubClient{turns: []stubTurn{{result: &deepseek.TurnResult{Content: "done"}}}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	got, err := runner.Run(context.Background(), session, "finish the task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Run() result = %q, want %q", got, "done")
	}
	if gotTypes := eventTypes(t, emitter.recordedEvents()); !reflect.DeepEqual(gotTypes, []string{
		"task_started",
		"agent_message",
		"task_complete",
	}) {
		t.Fatalf("event types = %v", gotTypes)
	}
	lastMessage := session.messages[len(session.messages)-1]
	if lastMessage.Role != openai.ChatMessageRoleAssistant || lastMessage.Content != "done" {
		t.Fatalf("last message = %#v", lastMessage)
	}
}

func TestRunnerIncludesReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "default", options: Options{}, want: "high"},
		{name: "explicit override", options: Options{ReasoningEffort: "low"}, want: "low"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &stubClient{turns: []stubTurn{{result: &deepseek.TurnResult{Content: "done"}}}}
			session := newTestSession(t, test.options)
			runner := &Runner{Client: client, Emitter: &recEmitter{}, Approver: &stubApprover{}}

			if _, err := runner.Run(context.Background(), session, "finish the task"); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			requests := client.recordedRequests()
			if len(requests) != 1 {
				t.Fatalf("request count = %d, want 1", len(requests))
			}
			if requests[0].ReasoningEffort != test.want {
				t.Fatalf("request reasoning effort = %q, want %q", requests[0].ReasoningEffort, test.want)
			}
		})
	}
}

func TestRunnerShellToolCallThenText(t *testing.T) {
	call := toolCall("call-shell", "shell", `{"command":"echo hi"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{call}}},
		{result: &deepseek.TurnResult{Content: "ok"}},
	}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{
		Cwd:      t.TempDir(),
		Sandbox:  policy.Sandbox("workspace-write"),
		Approval: policy.ApprovalPolicy("never"),
	})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	got, err := runner.Run(context.Background(), session, "say hi")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("Run() result = %q, want %q", got, "ok")
	}

	requests := client.recordedRequests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	toolMessage := findToolMessage(t, requests[1].Messages, call.ID)
	if !strings.Contains(toolMessage.Content, "hi") {
		t.Fatalf("tool result = %q, want it to contain hi", toolMessage.Content)
	}

	events := emitter.recordedEvents()
	if gotTypes := eventTypes(t, events); !reflect.DeepEqual(gotTypes, []string{
		"task_started",
		"exec_command_begin",
		"exec_command_end",
		"agent_message",
		"task_complete",
	}) {
		t.Fatalf("event types = %v", gotTypes)
	}
	begin := events[1]
	if begin["call_id"] != call.ID || begin["tool"] != "shell" || begin["command"] != "echo hi" {
		t.Fatalf("begin event = %#v", begin)
	}
	end := events[2]
	if end["call_id"] != call.ID || end["tool"] != "shell" || end["exit_code"] != 0 {
		t.Fatalf("end event = %#v", end)
	}
	if _, ok := end["error"]; ok {
		t.Fatalf("successful shell end event unexpectedly contains error: %#v", end)
	}
}

func TestRunnerEmptyFileResultHasNonEmptyToolContent(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "empty.txt"), nil, 0o644); err != nil {
		t.Fatalf("create empty file: %v", err)
	}

	call := toolCall("call-empty", "read_file", `{"path":"empty.txt"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{call}}},
		{result: &deepseek.TurnResult{Content: "empty file handled"}},
	}}
	session := newTestSession(t, Options{
		Cwd:      cwd,
		Sandbox:  policy.Sandbox("workspace-write"),
		Approval: policy.ApprovalPolicy("never"),
	})
	runner := &Runner{Client: client, Emitter: &recEmitter{}, Approver: &stubApprover{}}

	if _, err := runner.Run(context.Background(), session, "read empty.txt"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requests := client.recordedRequests()
	toolMessage := findToolMessage(t, requests[1].Messages, call.ID)
	if toolMessage.Content == "" {
		t.Fatal("empty file produced a tool message with empty Content")
	}
	if toolMessage.Content != "(empty output)" {
		t.Fatalf("empty file tool result = %q, want %q", toolMessage.Content, "(empty output)")
	}
}

func TestRunnerApprovalDenied(t *testing.T) {
	const justification = "the requested change needs a file write"
	call := toolCall("call-write", "write_file", `{"path":"out.txt","content":"hello","justification":"`+justification+`"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{call}}},
		{result: &deepseek.TurnResult{Content: "blocked but finished"}},
	}}
	emitter := &recEmitter{}
	approver := &stubApprover{approved: false}
	session := newTestSession(t, Options{
		Cwd:      t.TempDir(),
		Sandbox:  policy.Sandbox("read-only"),
		Approval: policy.ApprovalPolicy("on-request"),
	})
	runner := &Runner{Client: client, Emitter: emitter, Approver: approver}

	got, err := runner.Run(context.Background(), session, "write a file")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "blocked but finished" {
		t.Fatalf("Run() result = %q", got)
	}
	requests := client.recordedRequests()
	toolMessage := findToolMessage(t, requests[1].Messages, call.ID)
	if !strings.Contains(toolMessage.Content, "approval was not granted") {
		t.Fatalf("tool result = %q", toolMessage.Content)
	}
	assertNoExecEvents(t, emitter.recordedEvents())

	approvalRequests := approver.recordedRequests()
	if len(approvalRequests) != 1 {
		t.Fatalf("approval request count = %d, want 1", len(approvalRequests))
	}
	if !strings.Contains(approvalRequests[0].Reason, "read-only sandbox") ||
		!strings.Contains(approvalRequests[0].Reason, justification) {
		t.Fatalf("approval reason = %q, want policy reason and justification %q", approvalRequests[0].Reason, justification)
	}
	if approvalRequests[0].Tool != "write_file" || approvalRequests[0].Path != "out.txt" {
		t.Fatalf("approval request = %#v", approvalRequests[0])
	}
}

func TestRunnerRecoversToolExecutionPanicAndCompletesHistory(t *testing.T) {
	call := toolCall("call-panic", "shell", `{"command":"echo should-not-run"}`)
	afterPanicCall := toolCall("call-after-panic", "shell", `{"command":"echo after-panic"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{call, afterPanicCall}}},
		{result: &deepseek.TurnResult{Content: "recovered"}},
	}}
	recorder := &recEmitter{}
	emitter := &panicOnceEmitter{recorder: recorder, panicType: "exec_command_begin"}
	session := newTestSession(t, Options{
		Cwd:      t.TempDir(),
		Sandbox:  policy.Sandbox("workspace-write"),
		Approval: policy.ApprovalPolicy("never"),
	})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	got, err := runner.Run(context.Background(), session, "trigger a tool panic")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "recovered" {
		t.Fatalf("Run() result = %q, want %q", got, "recovered")
	}

	requests := client.recordedRequests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	toolMessage := findToolMessage(t, requests[1].Messages, call.ID)
	if !strings.Contains(toolMessage.Content, "tool execution panicked") ||
		!strings.Contains(toolMessage.Content, "emitter boom") {
		t.Fatalf("panic tool result = %q", toolMessage.Content)
	}
	afterPanicMessage := findToolMessage(t, requests[1].Messages, afterPanicCall.ID)
	if !strings.Contains(afterPanicMessage.Content, "after-panic") {
		t.Fatalf("post-panic tool result = %q", afterPanicMessage.Content)
	}
	assertCompleteToolHistory(t, requests[1].Messages)

	events := recorder.recordedEvents()
	if gotTypes := eventTypes(t, events); !reflect.DeepEqual(gotTypes, []string{
		"task_started",
		"exec_command_begin",
		"exec_command_end",
		"exec_command_begin",
		"exec_command_end",
		"agent_message",
		"task_complete",
	}) {
		t.Fatalf("event types = %v", gotTypes)
	}
	end := events[2]
	if end["call_id"] != call.ID || end["tool"] != "shell" || end["exit_code"] != -1 {
		t.Fatalf("panic end event = %#v", end)
	}
	if _, ok := end["error"]; ok {
		t.Fatalf("panic end event unexpectedly contains error: %#v", end)
	}
}

func TestRunnerNeverPolicyDenial(t *testing.T) {
	call := toolCall("call-rm", "shell", `{"command":"rm x"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{call}}},
		{result: &deepseek.TurnResult{Content: "not removed"}},
	}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{
		Cwd:      t.TempDir(),
		Sandbox:  policy.Sandbox("read-only"),
		Approval: policy.ApprovalPolicy("never"),
	})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{approved: true}}

	if _, err := runner.Run(context.Background(), session, "remove x"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requests := client.recordedRequests()
	toolMessage := findToolMessage(t, requests[1].Messages, call.ID)
	if !strings.Contains(toolMessage.Content, "denied by sandbox policy") {
		t.Fatalf("tool result = %q", toolMessage.Content)
	}
	assertNoExecEvents(t, emitter.recordedEvents())
}

func TestRunnerTurnLimitReachedThenResumed(t *testing.T) {
	firstCall := toolCall("call-one", "shell", `{"command":"echo one"}`)
	secondCall := toolCall("call-two", "shell", `{"command":"echo two"}`)
	client := &stubClient{turns: []stubTurn{
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{firstCall}}},
		{result: &deepseek.TurnResult{ToolCalls: []openai.ToolCall{secondCall}}},
		{result: &deepseek.TurnResult{Content: "resumed"}},
	}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{
		Cwd:      t.TempDir(),
		Sandbox:  policy.Sandbox("workspace-write"),
		Approval: policy.ApprovalPolicy("never"),
		MaxTurns: 2,
	})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	got, err := runner.Run(context.Background(), session, "keep using tools")
	if got != "" {
		t.Fatalf("first Run() result = %q, want empty", got)
	}
	if err == nil || !strings.Contains(err.Error(), "turn limit reached (2)") {
		t.Fatalf("first Run() error = %v", err)
	}
	if !containsEventType(t, emitter.recordedEvents(), "error") {
		t.Fatal("turn-limit run did not emit an error event")
	}

	got, err = runner.Run(context.Background(), session, "resume")
	if err != nil {
		t.Fatalf("resumed Run() error = %v", err)
	}
	if got != "resumed" {
		t.Fatalf("resumed Run() result = %q, want %q", got, "resumed")
	}
	if len(client.recordedRequests()) != 3 {
		t.Fatalf("request count after resume = %d, want 3", len(client.recordedRequests()))
	}
	assertCompleteToolHistory(t, client.recordedRequests()[2].Messages)
}

func TestExecToolCallRejectsInvalidModelOutput(t *testing.T) {
	runner := &Runner{}
	session := newTestSession(t, Options{})

	tests := []struct {
		name string
		call openai.ToolCall
		want string
	}{
		{
			name: "unknown tool",
			call: toolCall("call-unknown", "not_a_real_tool", `{}`),
			want: "unknown tool: not_a_real_tool",
		},
		{
			name: "malformed arguments",
			call: toolCall("call-malformed", "shell", `not-json`),
			want: "invalid tool arguments:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runner.execToolCall(context.Background(), session, test.call)
			if !strings.Contains(got, test.want) {
				t.Fatalf("execToolCall() = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestRunnerBusy(t *testing.T) {
	unblock := make(chan struct{})
	entered := make(chan struct{}, 1)
	client := &stubClient{
		turns:   []stubTurn{{result: &deepseek.TurnResult{Content: "first done"}}},
		block:   unblock,
		entered: entered,
	}
	session := newTestSession(t, Options{})
	runner := &Runner{Client: client, Emitter: &recEmitter{}, Approver: &stubApprover{}}

	firstResult := make(chan struct {
		answer string
		err    error
	}, 1)
	go func() {
		answer, err := runner.Run(context.Background(), session, "first")
		firstResult <- struct {
			answer string
			err    error
		}{answer: answer, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Run() did not reach the blocked client")
	}

	secondResult := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), session, "second")
		secondResult <- err
	}()
	select {
	case err := <-secondResult:
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("concurrent Run() error = %v, want ErrBusy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Run() blocked instead of returning ErrBusy")
	}

	close(unblock)
	select {
	case result := <-firstResult:
		if result.err != nil || result.answer != "first done" {
			t.Fatalf("first Run() = (%q, %v)", result.answer, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Run() did not finish after unblocking the client")
	}
}

func TestManagerBasics(t *testing.T) {
	manager := NewManager()
	if session, ok := manager.Get("missing"); ok || session != nil {
		t.Fatalf("Get(missing) = (%#v, %v), want (nil, false)", session, ok)
	}

	first := manager.Create(Options{})
	second := manager.Create(Options{})
	if first.ID == "" || second.ID == "" {
		t.Fatalf("Create() returned an empty ID: %q, %q", first.ID, second.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("Create() returned duplicate ID %q", first.ID)
	}
	if got, ok := manager.Get(first.ID); !ok || got != first {
		t.Fatalf("Get(first.ID) = (%#v, %v), want first session", got, ok)
	}
	if first.model != "deepseek-v4-pro" || first.maxTurns != DefaultMaxTurns {
		t.Fatalf("defaults = model %q, max turns %d", first.model, first.maxTurns)
	}
	if len(first.messages) != 1 || first.messages[0].Role != openai.ChatMessageRoleSystem || first.messages[0].Content != DefaultSystemPrompt {
		t.Fatalf("initial messages = %#v", first.messages)
	}
}

func TestManagerReasoningEffort(t *testing.T) {
	manager := NewManager()

	if got := manager.Create(Options{}).reasoningEffort; got != "high" {
		t.Fatalf("default reasoning effort = %q, want %q", got, "high")
	}
	if got := manager.Create(Options{ReasoningEffort: "low"}).reasoningEffort; got != "low" {
		t.Fatalf("explicit reasoning effort = %q, want %q", got, "low")
	}
}

func TestRunnerClientErrorPreservesSessionAndUnlocks(t *testing.T) {
	client := &stubClient{turns: []stubTurn{
		{err: errors.New("upstream failed")},
		{result: &deepseek.TurnResult{Content: "recovered"}},
	}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	if _, err := runner.Run(context.Background(), session, "first prompt"); err == nil || err.Error() != "upstream failed" {
		t.Fatalf("first Run() error = %v", err)
	}
	if len(session.messages) != 2 || session.messages[1].Content != "first prompt" {
		t.Fatalf("messages after client error = %#v", session.messages)
	}
	if gotTypes := eventTypes(t, emitter.recordedEvents()); !reflect.DeepEqual(gotTypes, []string{"task_started", "error"}) {
		t.Fatalf("first run event types = %v", gotTypes)
	}

	got, err := runner.Run(context.Background(), session, "second prompt")
	if err != nil || got != "recovered" {
		t.Fatalf("second Run() = (%q, %v)", got, err)
	}
	if len(session.messages) != 4 {
		t.Fatalf("message count after recovery = %d, want 4", len(session.messages))
	}
}

func TestRunnerEmitsDeltasAndUsageInOrder(t *testing.T) {
	client := &stubClient{turns: []stubTurn{{
		result: &deepseek.TurnResult{
			Content: "done",
			Usage: &openai.Usage{
				PromptTokens:     7,
				CompletionTokens: 5,
				TotalTokens:      12,
			},
		},
		deltas: []string{"do", "ne"},
	}}}
	emitter := &recEmitter{}
	session := newTestSession(t, Options{})
	runner := &Runner{Client: client, Emitter: emitter, Approver: &stubApprover{}}

	if _, err := runner.Run(context.Background(), session, "stream"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events := emitter.recordedEvents()
	if gotTypes := eventTypes(t, events); !reflect.DeepEqual(gotTypes, []string{
		"task_started",
		"agent_message_delta",
		"agent_message_delta",
		"token_count",
		"agent_message",
		"task_complete",
	}) {
		t.Fatalf("event types = %v", gotTypes)
	}
	if events[1]["delta"] != "do" || events[2]["delta"] != "ne" {
		t.Fatalf("delta events = %#v, %#v", events[1], events[2])
	}
	if events[3]["prompt_tokens"] != 7 || events[3]["completion_tokens"] != 5 || events[3]["total_tokens"] != 12 {
		t.Fatalf("token event = %#v", events[3])
	}
}

func TestBuiltinTools(t *testing.T) {
	tools := builtinTools()
	if len(tools) != 3 {
		t.Fatalf("builtin tool count = %d, want 3", len(tools))
	}

	wantRequired := map[string][]string{
		"shell":      {"command"},
		"read_file":  {"path"},
		"write_file": {"path", "content"},
	}
	for _, tool := range tools {
		if tool.Type != openai.ToolTypeFunction || tool.Function == nil {
			t.Fatalf("invalid tool declaration: %#v", tool)
		}
		definition := tool.Function
		if !strings.Contains(strings.ToLower(definition.Description), "sandbox policy") ||
			!strings.Contains(strings.ToLower(definition.Description), "justification") {
			t.Errorf("%s description does not explain sandbox/justification: %q", definition.Name, definition.Description)
		}
		raw, ok := definition.Parameters.(json.RawMessage)
		if !ok {
			t.Fatalf("%s Parameters type = %T, want json.RawMessage", definition.Name, definition.Parameters)
		}
		var schema struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", definition.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s schema type = %q", definition.Name, schema.Type)
		}
		if !reflect.DeepEqual(schema.Required, wantRequired[definition.Name]) {
			t.Errorf("%s required = %v, want %v", definition.Name, schema.Required, wantRequired[definition.Name])
		}
		if _, ok := schema.Properties["justification"]; !ok {
			t.Errorf("%s schema lacks justification", definition.Name)
		}
		if definition.Name == "shell" {
			timeout := schema.Properties["timeout_seconds"]
			if timeout.Type != "integer" || !strings.Contains(timeout.Description, "clamped, max 600") {
				t.Errorf("shell timeout_seconds = %#v", timeout)
			}
		}
	}
}

func newTestSession(t *testing.T, options Options) *Session {
	t.Helper()
	if options.Cwd == "" {
		options.Cwd = t.TempDir()
	}
	if options.Sandbox == "" {
		options.Sandbox = policy.Sandbox("workspace-write")
	}
	if options.Approval == "" {
		options.Approval = policy.ApprovalPolicy("never")
	}
	return NewManager().Create(options)
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

func findToolMessage(t *testing.T, messages []openai.ChatCompletionMessage, callID string) openai.ChatCompletionMessage {
	t.Helper()
	for _, message := range messages {
		if message.Role == openai.ChatMessageRoleTool && message.ToolCallID == callID {
			return message
		}
	}
	t.Fatalf("no tool message found for call ID %q in %#v", callID, messages)
	return openai.ChatCompletionMessage{}
}

func eventTypes(t *testing.T, events []map[string]any) []string {
	t.Helper()
	types := make([]string, len(events))
	for i, event := range events {
		eventType, ok := event["type"].(string)
		if !ok {
			t.Fatalf("event %d has invalid type: %#v", i, event)
		}
		types[i] = eventType
	}
	return types
}

func containsEventType(t *testing.T, events []map[string]any, want string) bool {
	t.Helper()
	for _, eventType := range eventTypes(t, events) {
		if eventType == want {
			return true
		}
	}
	return false
}

func assertNoExecEvents(t *testing.T, events []map[string]any) {
	t.Helper()
	for _, eventType := range eventTypes(t, events) {
		if eventType == "exec_command_begin" || eventType == "exec_command_end" {
			t.Fatalf("denied call emitted %s", eventType)
		}
	}
}

func assertCompleteToolHistory(t *testing.T, messages []openai.ChatCompletionMessage) {
	t.Helper()

	toolResponses := make(map[string][]openai.ChatCompletionMessage)
	for _, message := range messages {
		if message.Role != openai.ChatMessageRoleTool {
			continue
		}
		if message.Content == "" {
			t.Fatalf("tool response %q has empty Content", message.ToolCallID)
		}
		toolResponses[message.ToolCallID] = append(toolResponses[message.ToolCallID], message)
	}

	for _, message := range messages {
		if message.Role != openai.ChatMessageRoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			responses := toolResponses[call.ID]
			if len(responses) != 1 {
				t.Fatalf("tool call %q has %d responses, want exactly 1", call.ID, len(responses))
			}
		}
	}
}
