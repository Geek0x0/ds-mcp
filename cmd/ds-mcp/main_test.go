package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const mainChild = "DS_MCP_MAIN_CHILD"

func TestResolveAPIKeyFromEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-environment")

	key, err := resolveAPIKey()
	if err != nil {
		t.Fatalf("resolveAPIKey() error = %v", err)
	}
	if key != "sk-from-environment" {
		t.Fatalf("resolveAPIKey() = %q, want %q", key, "sk-from-environment")
	}
}

func TestResolveAPIKeyFromAuthFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "")
	writeAuthFile(t, home, `{"api_key":"sk-from-file","future_field":"ignored"}`, 0o600)

	key, err := resolveAPIKey()
	if err != nil {
		t.Fatalf("resolveAPIKey() error = %v", err)
	}
	if key != "sk-from-file" {
		t.Fatalf("resolveAPIKey() = %q, want %q", key, "sk-from-file")
	}
}

func TestMainRequiresDeepSeekAPIKey(t *testing.T) {
	if runMainChild(t) {
		return
	}

	output := runMainExpectingFailure(t, nil)
	const want = "DEEPSEEK_API_KEY environment variable or ~/.config/ds-mcp/auth.json is required"
	if !strings.Contains(output, want) {
		t.Fatalf("main() failure output = %q, want it to contain %q", output, want)
	}
}

func TestMainRejectsPermissiveAuthFile(t *testing.T) {
	if runMainChild(t) {
		return
	}

	output := runMainExpectingFailure(t, func(home string) {
		writeAuthFile(t, home, `{"api_key":"sk-from-file"}`, 0o644)
	})
	for _, want := range []string{"overly permissive permissions", "mode 644", "chmod 600"} {
		if !strings.Contains(output, want) {
			t.Fatalf("main() failure output = %q, want it to contain %q", output, want)
		}
	}
}

func TestMainRejectsMalformedAuthFile(t *testing.T) {
	if runMainChild(t) {
		return
	}

	output := runMainExpectingFailure(t, func(home string) {
		writeAuthFile(t, home, `{"api_key":`, 0o600)
	})
	if !strings.Contains(output, "invalid JSON") {
		t.Fatalf("main() failure output = %q, want it to contain %q", output, "invalid JSON")
	}
	assertNotGenericMissingKeyError(t, output)
}

func TestMainRejectsAuthFileWithoutAPIKey(t *testing.T) {
	if runMainChild(t) {
		return
	}

	output := runMainExpectingFailure(t, func(home string) {
		writeAuthFile(t, home, `{}`, 0o600)
	})
	if !strings.Contains(output, "empty or missing api_key") {
		t.Fatalf("main() failure output = %q, want it to contain %q", output, "empty or missing api_key")
	}
	assertNotGenericMissingKeyError(t, output)
}

func runMainChild(t *testing.T) bool {
	if os.Getenv(mainChild) != t.Name() {
		return false
	}
	main()
	return true
}

func runMainExpectingFailure(t *testing.T, setup func(home string)) string {
	t.Helper()
	home := t.TempDir()
	if setup != nil {
		setup(home)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	for _, entry := range os.Environ() {
		if !hasEnvName(entry, "DEEPSEEK_API_KEY") &&
			!hasEnvName(entry, "HOME") &&
			!hasEnvName(entry, mainChild) {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, mainChild+"="+t.Name())
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("main() accepted invalid API key configuration; child output:\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("main() error = %v, want a non-zero exit", err)
	}
	return string(output)
}

func writeAuthFile(t *testing.T, home, contents string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "ds-mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}
	return path
}

func hasEnvName(entry, name string) bool {
	return strings.HasPrefix(entry, name+"=")
}

func assertNotGenericMissingKeyError(t *testing.T, output string) {
	t.Helper()
	const generic = "environment variable or ~/.config/ds-mcp/auth.json is required"
	if strings.Contains(output, generic) {
		t.Fatalf("main() failure output = %q, do not want generic missing-key error", output)
	}
}
