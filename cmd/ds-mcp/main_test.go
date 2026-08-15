package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const missingAPIKeyChild = "DS_MCP_MISSING_API_KEY_CHILD"

func TestMainRequiresDeepSeekAPIKey(t *testing.T) {
	if os.Getenv(missingAPIKeyChild) == "1" {
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestMainRequiresDeepSeekAPIKey$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DEEPSEEK_API_KEY=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, missingAPIKeyChild+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("main() accepted a missing DEEPSEEK_API_KEY; child output:\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("main() error = %v, want a non-zero exit", err)
	}

	const want = "DEEPSEEK_API_KEY is required"
	if !strings.Contains(string(output), want) {
		t.Fatalf("main() failure output = %q, want it to contain %q", output, want)
	}
}
