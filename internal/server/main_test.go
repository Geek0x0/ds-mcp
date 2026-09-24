package server

import (
	"os"
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/sandbox"
)

func TestMain(m *testing.M) {
	sandbox.MaybeRunHelper()
	// Keep tests from writing into the developer's real ~/.codex.
	os.Setenv("SUBAGENT_MCP_ROLLOUT", "off")
	os.Exit(m.Run())
}
