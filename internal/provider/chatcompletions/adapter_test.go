package chatcompletions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/provider"
	"github.com/Geek0x0/subagent-mcp/internal/testutil"
)

func newAdapter(t *testing.T, fake *testutil.FakeChat) provider.Provider {
	t.Helper()
	p, err := New("deepseek", config.Provider{API: config.APIChatCompletions, BaseURL: fake.URL}, "key")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var shellSpec = provider.ToolSpec{
	Name:        "shell",
	Description: "run",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
}

func TestAdapterTextTurn(t *testing.T) {
	fake := testutil.NewFakeChat(t, []testutil.FakeTurn{{Text: "hello"}})
	p := newAdapter(t, fake)
	var deltas string
	res, err := p.Turn(context.Background(), provider.TurnRequest{
		Model: "m", Effort: "high", System: "sys",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
		Tools:    []provider.ToolSpec{shellSpec},
	}, func(d string) { deltas += d })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello" || deltas != "hello" || res.Usage == nil || res.Usage.Total != 12 {
		t.Fatalf("Turn() = %#v deltas=%q", res, deltas)
	}
	req := fake.Request(0)
	system := req["messages"].([]any)[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "sys" {
		t.Fatalf("system message = %#v", system)
	}
	if req["reasoning_effort"] != "high" || req["model"] != "m" {
		t.Fatalf("request = %#v", req)
	}
}

func TestAdapterReplaysReasoningAndToolResults(t *testing.T) {
	fake := testutil.NewFakeChat(t, []testutil.FakeTurn{
		{Reasoning: "think", ToolCalls: []testutil.FakeToolCall{{ID: "c1", Name: "shell", Args: `{"command":"ls"}`}}},
		{Text: "done"},
	})
	p := newAdapter(t, fake)
	history := []provider.Message{{Role: provider.RoleUser, Text: "go"}}
	first, err := p.Turn(context.Background(), provider.TurnRequest{Model: "m", Messages: history, Tools: []provider.ToolSpec{shellSpec}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reasoning != "think" || len(first.ToolCalls) != 1 || first.ToolCalls[0].Arguments != `{"command":"ls"}` {
		t.Fatalf("first = %#v", first)
	}
	history = append(history,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: first.ToolCalls, Opaque: first.Opaque},
		provider.Message{Role: provider.RoleTool, ToolCallID: "c1", Text: "a.txt", IsError: true},
	)
	if _, err := p.Turn(context.Background(), provider.TurnRequest{Model: "m", Messages: history, Tools: []provider.ToolSpec{shellSpec}}, nil); err != nil {
		t.Fatal(err)
	}
	// No System in this request, so there is no system message: [user, assistant, tool].
	messages := fake.Request(1)["messages"].([]any)
	assistant := messages[1].(map[string]any)
	if assistant["reasoning_content"] != "think" || assistant["tool_calls"] == nil {
		t.Fatalf("assistant replay = %#v", assistant)
	}
	tool := messages[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "c1" || tool["content"] != "a.txt" {
		t.Fatalf("tool replay = %#v", tool)
	}
}
