package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func ReadFile(ctx context.Context, cwd, path string) (string, error) {
	readCtx, cancel := context.WithTimeout(ctx, DefaultShellTimeout)
	defer cancel()

	type readResult struct {
		data      []byte
		totalSize int64
		err       error
	}

	resultCh := make(chan readResult, 1)
	fileCh := make(chan *os.File)
	// ponytail: os.Open is not context-aware, so a timeout can leave its goroutine blocked
	// until the open returns. A platform-specific nonblocking open plus polling could make
	// opening fully cancellable.
	go func() {
		file, err := os.Open(resolvePath(cwd, path))
		if err != nil {
			resultCh <- readResult{err: err}
			return
		}
		select {
		case fileCh <- file:
		case <-readCtx.Done():
			_ = file.Close()
			return
		}
		defer file.Close()

		var totalSize int64
		if info, statErr := file.Stat(); statErr == nil {
			totalSize = info.Size()
		}
		data, err := io.ReadAll(io.LimitReader(file, int64(MaxOutputBytes)+1))
		if totalSize < int64(len(data)) {
			totalSize = int64(len(data))
		}
		resultCh <- readResult{data: data, totalSize: totalSize, err: err}
	}()

	var file *os.File
	for {
		select {
		case file = <-fileCh:
			fileCh = nil
		case result := <-resultCh:
			if result.err != nil {
				return "", result.err
			}
			if len(result.data) <= MaxOutputBytes {
				return string(result.data), nil
			}
			return string(result.data[:MaxOutputBytes]) + fmt.Sprintf("\n[content truncated: %d bytes total]", result.totalSize), nil
		case <-readCtx.Done():
			if file != nil {
				_ = file.Close()
			}
			if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("file read timed out: %w", readCtx.Err())
			}
			return "", readCtx.Err()
		}
	}
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
