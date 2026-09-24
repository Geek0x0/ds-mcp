package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mainChild  = "SUBAGENT_MCP_MAIN_CHILD"
	testEnvKey = "SUBAGENT_TEST_API_KEY"

	validTestConfig = `
active_provider = "test"

[providers.test]
api = "chat-completions"
env_key = "SUBAGENT_TEST_API_KEY"
default_model = "test-model"
models = [{ id = "test-model" }]
`
)

func TestMainMissingConfigFile(t *testing.T) {
	if runMainChild(t) {
		return
	}

	missing := filepath.Join(t.TempDir(), "missing.toml")
	output := runMainExpectingFailure(t, "SUBAGENT_MCP_CONFIG="+missing)
	for _, want := range []string{missing, "config.example.toml"} {
		if !strings.Contains(output, want) {
			t.Fatalf("main() failure output = %q, want it to contain %q", output, want)
		}
	}
}

func TestMainRequiresActiveProviderKey(t *testing.T) {
	if runMainChild(t) {
		return
	}

	path := writeConfigFile(t, validTestConfig)
	output := runMainExpectingFailure(t, "SUBAGENT_MCP_CONFIG="+path)
	if !strings.Contains(output, testEnvKey) {
		t.Fatalf("main() failure output = %q, want it to name %q", output, testEnvKey)
	}
}

func TestMainVersion(t *testing.T) {
	if os.Getenv(mainChild) == t.Name() {
		os.Args = []string{os.Args[0], "--version"}
		main()
		os.Exit(0)
	}

	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	for _, entry := range os.Environ() {
		if !hasEnvName(entry, "HOME") &&
			!hasEnvName(entry, mainChild) &&
			!hasEnvName(entry, "SUBAGENT_MCP_CONFIG") &&
			!hasEnvName(entry, "SUBAGENT_MCP_TOOL_NAME") &&
			!hasEnvName(entry, testEnvKey) {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, mainChild+"="+t.Name())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("main() --version error = %v; stderr = %q", err, stderr.String())
	}
	if got, want := stdout.String(), "subagent-mcp "+version+"\n"; got != want {
		t.Fatalf("main() --version stdout = %q, want %q", got, want)
	}
}

func TestMainRejectsInvalidToolName(t *testing.T) {
	if runMainChild(t) {
		return
	}

	path := writeConfigFile(t, validTestConfig)
	output := runMainExpectingFailure(t,
		"SUBAGENT_MCP_CONFIG="+path,
		testEnvKey+"=sk-test",
		"SUBAGENT_MCP_TOOL_NAME=bad name",
	)
	if !strings.Contains(output, "SUBAGENT_MCP_TOOL_NAME") {
		t.Fatalf("main() failure output = %q, want it to mention %q", output, "SUBAGENT_MCP_TOOL_NAME")
	}
}

func runMainChild(t *testing.T) bool {
	if os.Getenv(mainChild) != t.Name() {
		return false
	}
	main()
	return true
}

func runMainExpectingFailure(t *testing.T, extraEnv ...string) string {
	t.Helper()
	home := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	for _, entry := range os.Environ() {
		if !hasEnvName(entry, "HOME") &&
			!hasEnvName(entry, mainChild) &&
			!hasEnvName(entry, "SUBAGENT_MCP_CONFIG") &&
			!hasEnvName(entry, "SUBAGENT_MCP_TOOL_NAME") &&
			!hasEnvName(entry, testEnvKey) {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, mainChild+"="+t.Name())
	cmd.Env = append(cmd.Env, extraEnv...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("main() accepted invalid configuration; child output:\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("main() error = %v, want a non-zero exit", err)
	}
	return string(output)
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}

func hasEnvName(entry, name string) bool {
	return strings.HasPrefix(entry, name+"=")
}
