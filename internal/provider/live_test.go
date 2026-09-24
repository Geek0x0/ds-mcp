package provider_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/provider"
	_ "github.com/Geek0x0/subagent-mcp/internal/provider/chatcompletions"
	_ "github.com/Geek0x0/subagent-mcp/internal/provider/messages"
	_ "github.com/Geek0x0/subagent-mcp/internal/provider/responses"
)

func TestLiveToolCallingRoundTrip(t *testing.T) {
	if os.Getenv("SUBAGENT_LIVE_TEST") != "1" {
		t.Skip("set SUBAGENT_LIVE_TEST=1 to run live provider tests")
	}
	cases := []struct {
		name, api, envKey, baseURL, modelEnv, defaultModel, effort string
	}{
		{"deepseek", config.APIChatCompletions, "DEEPSEEK_API_KEY", "https://api.deepseek.com", "SUBAGENT_LIVE_DEEPSEEK_MODEL", "deepseek-flash", "low"},
		{"openai", config.APIResponses, "OPENAI_API_KEY", "https://api.openai.com/v1", "SUBAGENT_LIVE_OPENAI_MODEL", "gpt-5.5", "low"},
		{"anthropic", config.APIMessages, "ANTHROPIC_API_KEY", "https://api.anthropic.com", "SUBAGENT_LIVE_ANTHROPIC_MODEL", "claude-sonnet-5", "low"},
	}
	tool := provider.ToolSpec{
		Name:        "get_secret_number",
		Description: "Returns the secret number. Always call this before answering.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := os.Getenv(tc.envKey)
			if key == "" {
				t.Skipf("%s not set", tc.envKey)
			}
			model := tc.defaultModel
			if override := os.Getenv(tc.modelEnv); override != "" {
				model = override
			}
			p, err := provider.New(tc.name, config.Provider{API: tc.api, BaseURL: tc.baseURL, MaxOutputTokens: 4096}, key)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			history := []provider.Message{{Role: provider.RoleUser, Text: "What is the secret number? Use the tool, then answer with just the number."}}
			first, err := p.Turn(ctx, provider.TurnRequest{Model: model, Effort: tc.effort, Messages: history, Tools: []provider.ToolSpec{tool}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.ToolCalls) == 0 {
				t.Fatalf("model did not call the tool: %#v", first)
			}
			history = append(history, provider.Message{Role: provider.RoleAssistant, Text: first.Text, ToolCalls: first.ToolCalls, Opaque: first.Opaque})
			for _, call := range first.ToolCalls {
				history = append(history, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Text: "4217"})
			}
			second, err := p.Turn(ctx, provider.TurnRequest{Model: model, Effort: tc.effort, Messages: history, Tools: []provider.ToolSpec{tool}}, nil)
			if err != nil {
				t.Fatalf("follow-up turn rejected (reasoning replay broken?): %v", err)
			}
			if !strings.Contains(second.Text, "4217") {
				t.Fatalf("answer = %q", second.Text)
			}
		})
	}
}
