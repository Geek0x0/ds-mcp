package testutil_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"ds-mcp/internal/testutil"
)

const ambiguousFakeTurnChild = "DS_MCP_AMBIGUOUS_FAKE_TURN_CHILD"

func TestNewFakeDeepSeekRejectsTextWithToolCalls(t *testing.T) {
	if os.Getenv(ambiguousFakeTurnChild) == "1" {
		testutil.NewFakeDeepSeek(t, []testutil.FakeTurn{{
			Text: "on it",
			ToolCalls: []testutil.FakeToolCall{{
				ID:   "call_ambiguous",
				Name: "get_weather",
				Args: `{"city":"Vancouver"}`,
			}},
		}})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNewFakeDeepSeekRejectsTextWithToolCalls$")
	cmd.Env = append(os.Environ(), ambiguousFakeTurnChild+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("NewFakeDeepSeek() accepted Text with ToolCalls; child output:\n%s", output)
	}

	const want = "FakeTurn 0: Text and ToolCalls are mutually exclusive"
	if !strings.Contains(string(output), want) {
		t.Fatalf("NewFakeDeepSeek() failure output = %q, want it to contain %q", output, want)
	}
}
