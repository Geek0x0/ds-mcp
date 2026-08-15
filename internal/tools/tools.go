package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const MaxOutputBytes = 16 * 1024

const (
	DefaultShellTimeout = 60 * time.Second
	MaxShellTimeout     = 600 * time.Second
)

type limitedBuffer struct {
	buf   bytes.Buffer
	total int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	if remaining := MaxOutputBytes - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	out := b.buf.String()
	if b.total > MaxOutputBytes {
		out += fmt.Sprintf("\n[output truncated: %d bytes total]", b.total)
	}
	return out
}

func RunShell(ctx context.Context, cwd, command string, timeout time.Duration) (out string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = DefaultShellTimeout
	} else if timeout > MaxShellTimeout {
		timeout = MaxShellTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var buf limitedBuffer
	cmd := exec.CommandContext(runCtx, "bash", "-lc", command)
	cmd.Dir = cwd
	// ponytail: Linux process-group signaling kills ordinary descendants, and WaitDelay bounds
	// inherited-pipe waits. A descendant that escapes the group can survive without being reported.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	out = buf.String()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return out, 0, nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return out, -1, fmt.Errorf("command timed out after %s", timeout)
	}
	if runCtx.Err() != nil {
		return out, -1, runCtx.Err()
	}
	if runErr == nil {
		return out, 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return out, exitErr.ExitCode(), nil
	}
	return out, -1, runErr
}

func ReadFile(cwd, path string) (string, error) {
	data, err := os.ReadFile(resolvePath(cwd, path))
	if err != nil {
		return "", err
	}
	if len(data) <= MaxOutputBytes {
		return string(data), nil
	}
	return string(data[:MaxOutputBytes]) + fmt.Sprintf("\n[content truncated: %d bytes total]", len(data)), nil
}

func WriteFile(cwd, path, content string) error {
	path = resolvePath(cwd, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}
