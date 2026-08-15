package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"ds-mcp/internal/deepseek"
	"ds-mcp/internal/testutil"

	openai "github.com/sashabaranov/go-openai"
)

func TestChatTurnStreamsTextAndUsage(t *testing.T) {
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{{Text: "hello world!"}})
	client := newTestClient(fake.URL)

	var deltas []string
	result, err := client.ChatTurn(context.Background(), chatRequest(), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("ChatTurn() error = %v", err)
	}

	if result.Content != "hello world!" {
		t.Errorf("Content = %q, want %q", result.Content, "hello world!")
	}
	if want := []string{"hello ", "world!"}; !reflect.DeepEqual(deltas, want) {
		t.Errorf("deltas = %#v, want %#v", deltas, want)
	}
	if result.Usage == nil {
		t.Fatal("Usage = nil, want populated usage")
	}
	if result.Usage.TotalTokens != 12 {
		t.Errorf("Usage.TotalTokens = %d, want 12", result.Usage.TotalTokens)
	}

	if fake.RequestCount() != 1 {
		t.Fatalf("RequestCount() = %d, want 1", fake.RequestCount())
	}
	request := fake.Request(0)
	if request["stream"] != true {
		t.Errorf("request stream = %#v, want true", request["stream"])
	}
	streamOptions, ok := request["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("request stream_options = %#v, want object", request["stream_options"])
	}
	if streamOptions["include_usage"] != true {
		t.Errorf("request stream_options.include_usage = %#v, want true", streamOptions["include_usage"])
	}
}

func TestChatTurnReconstructsToolCallsByIndex(t *testing.T) {
	calls := []testutil.FakeToolCall{
		{
			ID:   "call_weather",
			Name: "get_weather",
			Args: `{"city":"Vancouver","units":"metric"}`,
		},
		{
			ID:   "call_time",
			Name: "get_time",
			Args: `{"timezone":"America/Vancouver"}`,
		},
	}
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{{ToolCalls: calls}})
	client := newTestClient(fake.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err != nil {
		t.Fatalf("ChatTurn() error = %v", err)
	}

	if len(result.ToolCalls) != len(calls) {
		t.Fatalf("len(ToolCalls) = %d, want %d", len(result.ToolCalls), len(calls))
	}
	for i, want := range calls {
		got := result.ToolCalls[i]
		if got.ID != want.ID {
			t.Errorf("ToolCalls[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Function.Name != want.Name {
			t.Errorf("ToolCalls[%d].Function.Name = %q, want %q", i, got.Function.Name, want.Name)
		}
		if got.Function.Arguments != want.Args {
			t.Errorf("ToolCalls[%d].Function.Arguments = %q, want %q", i, got.Function.Arguments, want.Args)
		}

		var arguments map[string]any
		if err := json.Unmarshal([]byte(got.Function.Arguments), &arguments); err != nil {
			t.Errorf("ToolCalls[%d].Function.Arguments is not valid JSON: %v", i, err)
		}
	}
}

func TestChatTurnRetriesRetryableStatusesThenSucceeds(t *testing.T) {
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{
		{Status: 500},
		{Status: 429},
		{Text: "ok"},
	})
	client := newTestClient(fake.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err != nil {
		t.Fatalf("ChatTurn() error = %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("Content = %q, want %q", result.Content, "ok")
	}
	if fake.RequestCount() != 3 {
		t.Errorf("RequestCount() = %d, want 3", fake.RequestCount())
	}
}

func TestChatTurnReturnsErrorAfterRetriesAreExhausted(t *testing.T) {
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{
		{Status: 500},
		{Status: 500},
		{Status: 500},
		{Status: 500},
	})
	client := newTestClient(fake.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err == nil {
		t.Fatalf("ChatTurn() result = %#v, want non-nil error", result)
	}
	if fake.RequestCount() != 4 {
		t.Errorf("RequestCount() = %d, want 4", fake.RequestCount())
	}
}

func TestChatTurnDoesNotRetryNonRetryableStatus(t *testing.T) {
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{{Status: 400}})
	client := newTestClient(fake.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err == nil {
		t.Fatalf("ChatTurn() result = %#v, want non-nil error", result)
	}
	if fake.RequestCount() != 1 {
		t.Errorf("RequestCount() = %d, want 1", fake.RequestCount())
	}
}

func TestChatTurnStopsBackoffWhenContextIsCanceled(t *testing.T) {
	fake := testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{{Status: 500}})
	client := deepseek.New("test-key", fake.URL)
	ctx, cancel := context.WithCancel(context.Background())
	client.Backoff = func(int) time.Duration {
		cancel()
		return time.Hour
	}

	result, err := client.ChatTurn(ctx, chatRequest(), func(string) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ChatTurn() = (%#v, %v), want context.Canceled", result, err)
	}
	if fake.RequestCount() != 1 {
		t.Errorf("RequestCount() = %d, want 1", fake.RequestCount())
	}
}

func newTestClient(baseURL string) *deepseek.Client {
	client := deepseek.New("test-key", baseURL)
	client.Backoff = func(int) time.Duration { return 0 }

	return client
}

func chatRequest() openai.ChatCompletionRequest {
	return openai.ChatCompletionRequest{
		Model: "deepseek-chat",
		Messages: []openai.ChatCompletionMessage{{
			Role:    openai.ChatMessageRoleUser,
			Content: "hello",
		}},
	}
}
