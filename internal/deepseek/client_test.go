package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/Geek0x0/ds-mcp/internal/deepseek"
	"github.com/Geek0x0/ds-mcp/internal/testutil"

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

func TestChatTurnFallsBackToChunkPositionForIndexlessToolCall(t *testing.T) {
	server := newToolCallStream(t, openai.ToolCall{
		ID:   "call_indexless",
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "get_weather",
			Arguments: `{"city":"Vancouver"}`,
		},
	})
	client := newTestClient(server.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err != nil {
		t.Fatalf("ChatTurn() error = %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}

	got := result.ToolCalls[0]
	if got.ID != "call_indexless" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", got.ID, "call_indexless")
	}
	if got.Function.Name != "get_weather" {
		t.Errorf("ToolCalls[0].Function.Name = %q, want %q", got.Function.Name, "get_weather")
	}
	if got.Function.Arguments != `{"city":"Vancouver"}` {
		t.Errorf("ToolCalls[0].Function.Arguments = %q, want valid reconstructed arguments", got.Function.Arguments)
	}
}

func TestChatTurnDefaultsMissingToolCallTypeToFunction(t *testing.T) {
	index := 0
	server := newToolCallStream(t, openai.ToolCall{
		Index: &index,
		ID:    "call_typeless",
		Function: openai.FunctionCall{
			Name:      "get_time",
			Arguments: `{"timezone":"America/Vancouver"}`,
		},
	})
	client := newTestClient(server.URL)

	result, err := client.ChatTurn(context.Background(), chatRequest(), func(string) {})
	if err != nil {
		t.Fatalf("ChatTurn() error = %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Type != openai.ToolTypeFunction {
		t.Errorf("ToolCalls[0].Type = %q, want %q", result.ToolCalls[0].Type, openai.ToolTypeFunction)
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
	defer cancel()
	client.Backoff = func(int) time.Duration {
		time.AfterFunc(50*time.Millisecond, cancel)
		return time.Hour
	}

	type outcome struct {
		result *deepseek.TurnResult
		err    error
	}
	resultCh := make(chan outcome, 1)
	started := time.Now()
	go func() {
		result, err := client.ChatTurn(ctx, chatRequest(), func(string) {})
		resultCh <- outcome{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("ChatTurn() = (%#v, %v), want context.Canceled", got.result, got.err)
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			t.Errorf("ChatTurn() took %v, want cancellation well before the one-hour backoff", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("ChatTurn() did not stop promptly when context was canceled during backoff")
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

func newToolCallStream(t *testing.T, call openai.ToolCall) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}

		payload, err := json.Marshal(openai.ChatCompletionStreamResponse{
			Choices: []openai.ChatCompletionStreamChoice{{
				Delta: openai.ChatCompletionStreamChoiceDelta{
					ToolCalls: []openai.ToolCall{call},
				},
			}},
		})
		if err != nil {
			t.Errorf("marshal stream response: %v", err)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload); err != nil {
			t.Errorf("write stream response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}
