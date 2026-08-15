package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestReadFile(t *testing.T) {
	cwd := t.TempDir()

	t.Run("resolves relative path", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(cwd, "example.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatalf("WriteFile fixture: %v", err)
		}

		got, err := ReadFile(cwd, "example.txt")
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

		got, err := ReadFile(cwd, "large.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if len(got) <= MaxOutputBytes || len(got) >= MaxOutputBytes+100 {
			t.Errorf("len(ReadFile()) = %d, want slightly more than %d", len(got), MaxOutputBytes)
		}
		const marker = "[content truncated: 20000 bytes total]"
		if !strings.HasSuffix(got, marker) {
			t.Errorf("ReadFile() result suffix = %q, want %q", got[len(got)-len(marker):], marker)
		}
	})

	t.Run("returns error for nonexistent file", func(t *testing.T) {
		if _, err := ReadFile(cwd, "missing.txt"); err == nil {
			t.Fatal("ReadFile() error = nil, want an error")
		}
	})
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
