package server

import (
	"os"
	"testing"

	"github.com/Geek0x0/ds-mcp/internal/sandbox"
)

func TestMain(m *testing.M) {
	sandbox.MaybeRunHelper()
	os.Exit(m.Run())
}
