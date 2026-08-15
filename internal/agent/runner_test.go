package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ds-mcp/internal/deepseek"
	"ds-mcp/internal/policy"

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
	if approvalRequests[0].Reason != justification {
		t.Fatalf("approval reason = %q, want %q", approvalRequests[0].Reason, justification)
	}
	if approvalRequests[0].Tool != "write_file" || approvalRequests[0].Path != "out.txt" {
		t.Fatalf("approval request = %#v", approvalRequests[0])
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
	if first.model != "deepseek-chat" || first.maxTurns != DefaultMaxTurns {
		t.Fatalf("defaults = model %q, max turns %d", first.model, first.maxTurns)
	}
	if len(first.messages) != 1 || first.messages[0].Role != openai.ChatMessageRoleSystem || first.messages[0].Content != DefaultSystemPrompt {
		t.Fatalf("initial messages = %#v", first.messages)
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
