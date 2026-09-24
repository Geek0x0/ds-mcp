package tools

import (
	"os"
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/sandbox"
)

func TestMain(m *testing.M) {
	sandbox.MaybeRunHelper()
	os.Exit(m.Run())
}
