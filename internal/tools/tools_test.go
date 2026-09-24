package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Geek0x0/subagent-mcp/internal/sandbox"
)

func TestRunShell(t *testing.T) {
	t.Run("runs command in working directory", func(t *testing.T) {
		cwd := t.TempDir()

		out, exitCode, err := RunShell(context.Background(), cwd, "echo hello; pwd", time.Second)
		if err != nil {
			t.Fatalf("RunShell() error = %v", err)
		}
		if exitCode != 0 {
			t.Fatalf("RunShell() exitCode = %d, want 0", exitCode)
		}

		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || lines[0] != "hello" {
			t.Fatalf("RunShell() output = %q, want hello followed by cwd", out)
		}

		wantCWD, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", cwd, err)
		}
		gotCWD, err := filepath.EvalSymlinks(lines[1])
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", lines[1], err)
		}
		if gotCWD != wantCWD {
			t.Errorf("pwd = %q, want %q", gotCWD, wantCWD)
		}
	})

	t.Run("returns non-zero exit code without error", func(t *testing.T) {
		out, exitCode, err := RunShell(context.Background(), t.TempDir(), "exit 3", time.Second)
		if err != nil {
			t.Fatalf("RunShell() error = %v", err)
		}
		if exitCode != 3 {
			t.Errorf("RunShell() exitCode = %d, want 3", exitCode)
		}
		if out != "" {
			t.Errorf("RunShell() output = %q, want empty", out)
		}
	})

	t.Run("merges stdout and stderr", func(t *testing.T) {
		out, exitCode, err := RunShell(context.Background(), t.TempDir(), "echo out; echo err 1>&2", time.Second)
		if err != nil {
			t.Fatalf("RunShell() error = %v", err)
		}
		if exitCode != 0 {
			t.Fatalf("RunShell() exitCode = %d, want 0", exitCode)
		}
		if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
			t.Errorf("RunShell() output = %q, want stdout and stderr", out)
		}
	})

	t.Run("kills process group on timeout", func(t *testing.T) {
		start := time.Now()
		out, exitCode, err := RunShell(context.Background(), t.TempDir(), "sleep 5", 100*time.Millisecond)
		elapsed := time.Since(start)

		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("RunShell() error = %v, want timed out error", err)
		}
		if exitCode != -1 {
			t.Errorf("RunShell() exitCode = %d, want -1", exitCode)
		}
		if out != "" {
			t.Errorf("RunShell() output = %q, want empty", out)
		}
		if elapsed >= time.Second {
			t.Errorf("RunShell() elapsed = %v, want less than 1s", elapsed)
		}
	})

	t.Run("returns success when a background process holds the pipe", func(t *testing.T) {
		cwd := t.TempDir()
		command := "sleep 5 & echo $! > child.pid; echo done"

		start := time.Now()
		out, exitCode, err := RunShell(context.Background(), cwd, command, 200*time.Millisecond)
		elapsed := time.Since(start)

		childPID := readPIDFile(t, filepath.Join(cwd, "child.pid"))
		t.Cleanup(func() {
			if killErr := syscall.Kill(childPID, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				t.Errorf("kill background process %d: %v", childPID, killErr)
			}
		})

		if err != nil {
			t.Fatalf("RunShell() error = %v", err)
		}
		if exitCode != 0 {
			t.Errorf("RunShell() exitCode = %d, want 0", exitCode)
		}
		if !strings.Contains(out, "done") {
			t.Errorf("RunShell() output = %q, want done", out)
		}
		if elapsed >= 2*time.Second {
			t.Errorf("RunShell() elapsed = %v, want less than 2s", elapsed)
		}
		if killErr := syscall.Kill(childPID, 0); killErr != nil {
			t.Errorf("background process %d did not outlive bash: %v", childPID, killErr)
		}
	})

	t.Run("kills a grandchild and bounds inherited pipe wait", func(t *testing.T) {
		cwd := t.TempDir()
		command := "sleep 5 & echo $! > child.pid; setsid sleep 5 & echo $! > escaped.pid; wait"

		start := time.Now()
		out, exitCode, err := RunShell(context.Background(), cwd, command, 200*time.Millisecond)
		elapsed := time.Since(start)

		childPID := readPIDFile(t, filepath.Join(cwd, "child.pid"))
		escapedPID := readPIDFile(t, filepath.Join(cwd, "escaped.pid"))
		t.Cleanup(func() {
			if killErr := syscall.Kill(escapedPID, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				t.Errorf("kill escaped descendant %d: %v", escapedPID, killErr)
			}
		})

		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("RunShell() error = %v, want timed out error", err)
		}
		if exitCode != -1 {
			t.Errorf("RunShell() exitCode = %d, want -1", exitCode)
		}
		if out != "" {
			t.Errorf("RunShell() output = %q, want empty", out)
		}
		if elapsed >= 2*time.Second {
			t.Errorf("RunShell() elapsed = %v, want less than 2s", elapsed)
		}
		if !waitForProcessExit(childPID, time.Second) {
			t.Errorf("process-group grandchild %d is still alive", childPID)
		}
	})

	t.Run("truncates output", func(t *testing.T) {
		out, exitCode, err := RunShell(
			context.Background(),
			t.TempDir(),
			"head -c 20000 /dev/zero | tr '\\0' 'a'",
			time.Second,
		)
		if err != nil {
			t.Fatalf("RunShell() error = %v", err)
		}
		if exitCode != 0 {
			t.Fatalf("RunShell() exitCode = %d, want 0", exitCode)
		}
		if len(out) <= MaxOutputBytes || len(out) >= MaxOutputBytes+100 {
			t.Errorf("len(RunShell() output) = %d, want slightly more than %d", len(out), MaxOutputBytes)
		}
		const marker = "[output truncated: 20000 bytes total]"
		if !strings.HasSuffix(out, marker) {
			t.Errorf("RunShell() output suffix = %q, want %q", out[len(out)-len(marker):], marker)
		}
	})
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse PID from %q: %v", path, err)
	}
	return pid
}

func TestRunShellScrubsDeepSeekEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-secret")
	t.Setenv("DEEPSEEK_BASE_URL", "https://example.invalid")
	t.Setenv("SUBAGENT_MCP_KEEP_ME", "kept")

	out, exitCode, err := RunShell(context.Background(), t.TempDir(), "env", 5*time.Second)
	if err != nil {
		t.Fatalf("RunShell() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("RunShell() exitCode = %d, want 0", exitCode)
	}
	if strings.Contains(out, "sk-test-secret") {
		t.Errorf("RunShell() output leaked DEEPSEEK_API_KEY value: %q", out)
	}
	if strings.Contains(out, "DEEPSEEK_") {
		t.Errorf("RunShell() output contains a DEEPSEEK_ variable: %q", out)
	}
	if !strings.Contains(out, "SUBAGENT_MCP_KEEP_ME=kept") {
		t.Errorf("RunShell() output does not contain SUBAGENT_MCP_KEEP_ME=kept: %q", out)
	}
}

func TestRunShellSandboxedScrubsDeepSeekEnv(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-secret")
	t.Setenv("DEEPSEEK_BASE_URL", "https://example.invalid")
	t.Setenv("SUBAGENT_MCP_KEEP_ME", "kept")

	cwd := t.TempDir()
	out, exitCode, err := RunShellSandboxed(context.Background(), cwd, "env", 5*time.Second, []string{cwd})
	if err != nil {
		t.Fatalf("RunShellSandboxed() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("RunShellSandboxed() exitCode = %d, want 0", exitCode)
	}
	if strings.Contains(out, "sk-test-secret") {
		t.Errorf("RunShellSandboxed() output leaked DEEPSEEK_API_KEY value: %q", out)
	}
	if strings.Contains(out, "DEEPSEEK_") {
		t.Errorf("RunShellSandboxed() output contains a DEEPSEEK_ variable: %q", out)
	}
	if !strings.Contains(out, "SUBAGENT_MCP_KEEP_ME=kept") {
		t.Errorf("RunShellSandboxed() output does not contain SUBAGENT_MCP_KEEP_ME=kept: %q", out)
	}
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReadFile(t *testing.T) {
	cwd := t.TempDir()

	t.Run("resolves relative path", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(cwd, "example.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatalf("WriteFile fixture: %v", err)
		}

		got, err := ReadFile(context.Background(), cwd, "example.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if got != "hello" {
			t.Errorf("ReadFile() = %q, want %q", got, "hello")
		}
	})

	t.Run("truncates large file", func(t *testing.T) {
		const size = 20000
		path := filepath.Join(cwd, "large.txt")
		if err := os.WriteFile(path, []byte(strings.Repeat("a", size)), 0o644); err != nil {
			t.Fatalf("WriteFile fixture: %v", err)
		}

		got, err := ReadFile(context.Background(), cwd, "large.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if len(got) <= MaxOutputBytes || len(got) >= MaxOutputBytes+100 {
			t.Errorf("len(ReadFile()) = %d, want slightly more than %d", len(got), MaxOutputBytes)
		}
		const marker = "[content truncated: 20000 bytes total; continue with offset=2]"
		if !strings.HasSuffix(got, marker) {
			t.Errorf("ReadFile() result suffix = %q, want %q", got[len(got)-len(marker):], marker)
		}
	})

	t.Run("bounds read of sparse large file", func(t *testing.T) {
		const size int64 = 256 * 1024 * 1024
		path := filepath.Join(cwd, "sparse-large.txt")
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("Create fixture: %v", err)
		}
		if err := file.Truncate(size); err != nil {
			_ = file.Close()
			t.Fatalf("Truncate fixture: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close fixture: %v", err)
		}

		start := time.Now()
		got, err := ReadFile(context.Background(), cwd, "sparse-large.txt")
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		const marker = "[content truncated: 268435456 bytes total; continue with offset=2]"
		if !strings.HasSuffix(got, marker) {
			t.Errorf("ReadFile() result does not end with %q", marker)
		}
		if elapsed >= time.Second {
			t.Errorf("ReadFile() elapsed = %v, want less than 1s", elapsed)
		}
	})

	t.Run("times out reading FIFO without writer", func(t *testing.T) {
		path := filepath.Join(cwd, "blocked.fifo")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("Mkfifo fixture: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := ReadFile(ctx, cwd, "blocked.fifo")
		elapsed := time.Since(start)

		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("ReadFile() error = %v, want timed out error", err)
		}
		if elapsed >= time.Second {
			t.Errorf("ReadFile() elapsed = %v, want less than 1s", elapsed)
		}
	})

	t.Run("returns error for nonexistent file", func(t *testing.T) {
		if _, err := ReadFile(context.Background(), cwd, "missing.txt"); err == nil {
			t.Fatal("ReadFile() error = nil, want an error")
		}
	})
}

func TestReadFileRange(t *testing.T) {
	cwd := t.TempDir()
	const content = "l1\nl2\nl3\nl4\nl5\n"
	if err := os.WriteFile(filepath.Join(cwd, "lines.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile fixture: %v", err)
	}

	tests := []struct {
		name   string
		offset int
		limit  int
		want   string
	}{
		{
			name:   "line limit stops before end of file",
			offset: 2,
			limit:  2,
			want:   "l2\nl3\n\n[more lines follow; continue with offset=4]",
		},
		{
			name:   "offset to last lines",
			offset: 4,
			limit:  0,
			want:   "l4\nl5\n",
		},
		{
			name:   "zero offset reads whole file",
			offset: 0,
			limit:  0,
			want:   content,
		},
		{
			name:   "offset past end of file",
			offset: 9,
			limit:  0,
			want:   "[offset 9 is past the end of the file (5 lines)]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadFileRange(context.Background(), cwd, "lines.txt", tt.offset, tt.limit)
			if err != nil {
				t.Fatalf("ReadFileRange(offset=%d, limit=%d) error = %v", tt.offset, tt.limit, err)
			}
			if got != tt.want {
				t.Errorf("ReadFileRange(offset=%d, limit=%d) = %q, want %q", tt.offset, tt.limit, got, tt.want)
			}
		})
	}
}

func TestReadFileRangeByteCapKeepsWholeLines(t *testing.T) {
	cwd := t.TempDir()
	var fileContent strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&fileContent, "%09d\n", i)
	}
	if err := os.WriteFile(filepath.Join(cwd, "many-lines.txt"), []byte(fileContent.String()), 0o644); err != nil {
		t.Fatalf("WriteFile fixture: %v", err)
	}

	got, err := ReadFileRange(context.Background(), cwd, "many-lines.txt", 1, 0)
	if err != nil {
		t.Fatalf("ReadFileRange() error = %v", err)
	}

	const marker = "\n[content truncated: 30000 bytes total; continue with offset=1639]"
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("ReadFileRange() does not end with %q", marker)
	}
	head := strings.TrimSuffix(got, marker)
	if len(head) > MaxOutputBytes {
		t.Errorf("content length = %d, want <= %d", len(head), MaxOutputBytes)
	}
	if !strings.HasSuffix(head, "\n") {
		t.Errorf("content does not end in a whole line: %q", head)
	}
	// 16384/10 = 1638 whole 10-byte lines fit in the cap.
	if want := 1638 * 10; len(head) != want {
		t.Errorf("content length = %d, want %d", len(head), want)
	}

	next, err := ReadFileRange(context.Background(), cwd, "many-lines.txt", 1639, 0)
	if err != nil {
		t.Fatalf("ReadFileRange(offset=1639) error = %v", err)
	}
	firstLine := fmt.Sprintf("%09d\n", 1639)
	if !strings.HasPrefix(next, firstLine) {
		t.Errorf("ReadFileRange(offset=1639) does not start with line 1639 (%q)", firstLine)
	}
}

func TestReadFileRangeSingleHugeLine(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "huge-line.txt"), []byte(strings.Repeat("a", 20000)), 0o644); err != nil {
		t.Fatalf("WriteFile fixture: %v", err)
	}

	got, err := ReadFileRange(context.Background(), cwd, "huge-line.txt", 1, 0)
	if err != nil {
		t.Fatalf("ReadFileRange() error = %v", err)
	}

	const marker = "\n[content truncated: 20000 bytes total; continue with offset=2]"
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("ReadFileRange() does not end with %q", marker)
	}
	line := strings.TrimSuffix(got, marker)
	if len(line) != MaxOutputBytes {
		t.Errorf("content length = %d, want %d", len(line), MaxOutputBytes)
	}
	if line != strings.Repeat("a", MaxOutputBytes) {
		t.Errorf("content is not the first %d bytes of the line", MaxOutputBytes)
	}
}

func TestReadFileRefusesCredentialFile(t *testing.T) {
	const refusal = "refusing to read the ds-mcp credential file"

	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "ds-mcp")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", configDir, err)
	}
	credentialPath := filepath.Join(configDir, "auth.json")
	if err := os.WriteFile(credentialPath, []byte(`{"api_key":"sk-test-secret"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", credentialPath, err)
	}

	if _, err := ReadFile(context.Background(), t.TempDir(), credentialPath); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("ReadFile() error = %v, want error containing %q", err, refusal)
	}

	linkDir := t.TempDir()
	if err := os.Symlink(credentialPath, filepath.Join(linkDir, "link.json")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := ReadFile(context.Background(), linkDir, "link.json"); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("ReadFile() symlink error = %v, want error containing %q", err, refusal)
	}

	otherPath := filepath.Join(configDir, "notes.txt")
	if err := os.WriteFile(otherPath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", otherPath, err)
	}
	got, err := ReadFile(context.Background(), home, filepath.Join(".config", "ds-mcp", "notes.txt"))
	if err != nil {
		t.Fatalf("ReadFile() unrelated file error = %v", err)
	}
	if got != "ok" {
		t.Errorf("ReadFile() unrelated file = %q, want %q", got, "ok")
	}
}

func TestWriteFile(t *testing.T) {
	cwd := t.TempDir()
	relativePath := filepath.Join("a", "b", "c.txt")
	absolutePath := filepath.Join(cwd, relativePath)

	if err := WriteFile(cwd, relativePath, "first"); err != nil {
		t.Fatalf("WriteFile() creating nested path: %v", err)
	}
	got, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", absolutePath, err)
	}
	if string(got) != "first" {
		t.Errorf("file content = %q, want %q", got, "first")
	}

	if err := WriteFile(cwd, relativePath, "second"); err != nil {
		t.Fatalf("WriteFile() overwriting file: %v", err)
	}
	got, err = os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", absolutePath, err)
	}
	if string(got) != "second" {
		t.Errorf("overwritten file content = %q, want %q", got, "second")
	}
}

func TestRunShellSandboxed(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	cwd := t.TempDir()
	outside := t.TempDir()

	out, code, err := RunShellSandboxed(context.Background(), cwd, "touch inside && echo ok", 10*time.Second, []string{cwd})
	if err != nil || code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("inside = (%q, %d, %v)", out, code, err)
	}

	out, code, err = RunShellSandboxed(context.Background(), cwd, "touch "+filepath.Join(outside, "bad"), 10*time.Second, []string{cwd})
	if err != nil || code == 0 {
		t.Fatalf("outside = (%q, %d, %v), want non-zero exit", out, code, err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "bad")); statErr == nil {
		t.Fatalf("outside file was created")
	}
}

func TestRunShellSandboxedTimeoutKillsCommand(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	start := time.Now()
	_, code, err := RunShellSandboxed(context.Background(), t.TempDir(), "sleep 5", 200*time.Millisecond, nil)
	if elapsed := time.Since(start); err == nil || code != -1 || elapsed > 2*time.Second {
		t.Fatalf("RunShellSandboxed() = (%d, %v) after %v, want timeout", code, err, elapsed)
	}
}
