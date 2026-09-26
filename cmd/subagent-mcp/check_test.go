package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/provider"
	"github.com/Geek0x0/subagent-mcp/internal/testutil"
)

const (
	checkKeyA = "SUBAGENT_CHECK_KEY_A"
	checkKeyB = "SUBAGENT_CHECK_KEY_B"

	checkKeyValueA = "sk-check-secret-a-6b1f9c"
	checkKeyValueB = "sk-check-secret-b-2d7e4a"
)

func init() {
	provider.Register("check-no-lister", func(name string, _ config.Provider, _ string) (provider.Provider, error) {
		return &checkStubProvider{name: name}, nil
	})
}

// checkStubProvider is a Provider without the optional ModelLister capability.
type checkStubProvider struct {
	name  string
	calls int
}

func (p *checkStubProvider) Name() string { return p.name }

func (p *checkStubProvider) Turn(context.Context, provider.TurnRequest, func(string)) (*provider.TurnResult, error) {
	p.calls++
	if p.calls == 1 {
		return &provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "get_secret_number", Arguments: "{}"}}}, nil
	}
	return &provider.TurnResult{Text: "The secret number is 4217."}, nil
}

func chatProvider(baseURL, envKey, defaultModel string, modelIDs ...string) config.Provider {
	models := make([]config.Model, len(modelIDs))
	for i, id := range modelIDs {
		models[i] = config.Model{ID: id}
	}
	return config.Provider{
		API:             config.APIChatCompletions,
		BaseURL:         baseURL,
		EnvKey:          envKey,
		DefaultModel:    defaultModel,
		Models:          models,
		MaxOutputTokens: 4096,
	}
}

func assertNoKeyLeak(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(output, value) {
			t.Errorf("runCheck() output contains a configured key value; only variable names may be printed:\n%s", output)
		}
	}
}

func TestRunCheckAllProvidersHealthy(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)
	t.Setenv(checkKeyB, checkKeyValueB)

	chat := testutil.NewFakeChat(t, nil)
	chat.SetModels([]string{"deepseek-flash", "deepseek-v4-pro"})
	messages := testutil.NewFakeMessages(t, nil)
	messages.SetModels([]string{"claude-sonnet-5"})

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider(chat.URL, checkKeyA, "deepseek-flash", "deepseek-flash", "deepseek-v4-pro"),
			"anthropic": {
				API:             config.APIMessages,
				BaseURL:         messages.URL,
				EnvKey:          checkKeyB,
				DefaultModel:    "claude-sonnet-5",
				Models:          []config.Model{{ID: "claude-sonnet-5"}},
				MaxOutputTokens: 4096,
			},
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, false); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"provider anthropic (messages)",
		"provider deepseek (chat-completions)",
		"  key    " + checkKeyA + "   set",
		"  api    " + chat.URL + "   OK (2 models listed)",
		"  model  deepseek-flash     OK",
		"  model  deepseek-v4-pro     OK",
		"  key    " + checkKeyB + "   set",
		"  api    " + messages.URL + "   OK (1 models listed)",
		"  model  claude-sonnet-5     OK",
		"result   PASS (2 checked, 0 skipped, 0 failed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FAIL") {
		t.Errorf("runCheck() output contains FAIL:\n%s", got)
	}
	assertNoKeyLeak(t, got, checkKeyValueA, checkKeyValueB)
}

func TestRunCheckAllKeysUnsetFails(t *testing.T) {
	t.Setenv(checkKeyA, "")
	t.Setenv(checkKeyB, "")

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider("https://api.deepseek.invalid", checkKeyA, "deepseek-flash", "deepseek-flash"),
			"openai": {
				API:          config.APIResponses,
				BaseURL:      "https://api.openai.invalid",
				EnvKey:       checkKeyB,
				DefaultModel: "gpt-test",
				Models:       []config.Model{{ID: "gpt-test"}},
			},
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, false); code != 1 {
		t.Fatalf("runCheck() = %d, want 1; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"  key    " + checkKeyA + "   not set, skipped",
		"  key    " + checkKeyB + "   not set, skipped",
		"result   FAIL (0 checked, 2 skipped, 0 failed) — no configured provider has its key set",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "  api    ") {
		t.Errorf("runCheck() contacted a provider with no key:\n%s", got)
	}
}

func TestRunCheckSkippedProviderDoesNotFail(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)
	t.Setenv(checkKeyB, "")

	chat := testutil.NewFakeChat(t, nil)
	chat.SetModels([]string{"deepseek-flash"})

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider(chat.URL, checkKeyA, "deepseek-flash", "deepseek-flash"),
			"anthropic": {
				API:          config.APIMessages,
				BaseURL:      "https://api.anthropic.invalid",
				EnvKey:       checkKeyB,
				DefaultModel: "claude-test",
				Models:       []config.Model{{ID: "claude-test"}},
			},
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, false); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	if want := "  key    " + checkKeyB + "   not set, skipped"; !strings.Contains(got, want) {
		t.Errorf("runCheck() output missing %q:\n%s", want, got)
	}
	if want := "result   PASS (1 checked, 1 skipped, 0 failed)"; !strings.Contains(got, want) {
		t.Errorf("runCheck() output missing %q:\n%s", want, got)
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}

func TestRunCheckModelListingFailureSkipsLive(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)

	chat := testutil.NewFakeChat(t, []testutil.FakeTurn{{Text: "never reached"}})
	chat.SetModelsStatus(http.StatusUnauthorized)

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider(chat.URL, checkKeyA, "deepseek-flash", "deepseek-flash"),
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, true); code != 1 {
		t.Fatalf("runCheck() = %d, want 1; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"--live will make real, billed API calls to each configured provider.",
		"  api    " + chat.URL + "   FAIL: ",
		"401",
		"result   FAIL (1 checked, 0 skipped, 1 failed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "  live   ") {
		t.Errorf("runCheck() attempted a live turn after a listing failure:\n%s", got)
	}
	if requests := chat.RequestCount(); requests != 0 {
		t.Errorf("provider received %d turn requests, want 0", requests)
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}

func TestRunCheckMissingModelWarnsOnly(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)

	chat := testutil.NewFakeChat(t, nil)
	chat.SetModels([]string{"deepseek-flash"})

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider(chat.URL, checkKeyA, "deepseek-flash", "deepseek-flash", "deepseek-v4-pro"),
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, false); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"  model  deepseek-flash     OK",
		"  model  deepseek-v4-pro     WARN: not in the provider's current model list",
		"result   PASS (1 checked, 0 skipped, 0 failed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FAIL") {
		t.Errorf("runCheck() output contains FAIL:\n%s", got)
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}

func TestRunCheckLiveRoundTripSucceeds(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)

	chat := testutil.NewFakeChat(t, []testutil.FakeTurn{
		{ToolCalls: []testutil.FakeToolCall{{ID: "call_1", Name: "get_secret_number", Args: "{}"}}},
		{Text: "The secret number is 4217."},
	})
	chat.SetModels([]string{"deepseek-flash"})

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": chatProvider(chat.URL, checkKeyA, "deepseek-flash", "deepseek-flash"),
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, true); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"--live will make real, billed API calls to each configured provider.",
		"  live   deepseek-flash   OK (tool call + follow-up succeeded, ",
		"result   PASS (1 checked, 0 skipped, 0 failed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	if requests := chat.RequestCount(); requests != 2 {
		t.Errorf("provider received %d turn requests, want 2", requests)
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}

func TestRunCheckWithoutModelLister(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"custom": {
				API:          "check-no-lister",
				BaseURL:      "https://check.invalid",
				EnvKey:       checkKeyA,
				DefaultModel: "custom-model",
				Models:       []config.Model{{ID: "custom-model"}},
			},
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, false); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	if want := "  api    https://check.invalid   OK (model listing not supported)"; !strings.Contains(got, want) {
		t.Errorf("runCheck() output missing %q:\n%s", want, got)
	}
	if strings.Contains(got, "  model  ") {
		t.Errorf("runCheck() printed model lines without a model list:\n%s", got)
	}
	if want := "result   PASS (1 checked, 0 skipped, 0 failed)"; !strings.Contains(got, want) {
		t.Errorf("runCheck() output missing %q:\n%s", want, got)
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}

func TestRunCheckLiveWithoutModelLister(t *testing.T) {
	t.Setenv(checkKeyA, checkKeyValueA)

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"custom": {
				API:          "check-no-lister",
				BaseURL:      "https://check.invalid",
				EnvKey:       checkKeyA,
				DefaultModel: "custom-model",
				Models:       []config.Model{{ID: "custom-model"}},
			},
		},
	}

	var out bytes.Buffer
	if code := runCheck(&out, cfg, true); code != 0 {
		t.Fatalf("runCheck() = %d, want 0; output:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"--live will make real, billed API calls to each configured provider.",
		"  live   custom-model   OK (tool call + follow-up succeeded, ",
		"result   PASS (1 checked, 0 skipped, 0 failed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runCheck() output missing %q:\n%s", want, got)
		}
	}
	assertNoKeyLeak(t, got, checkKeyValueA)
}
