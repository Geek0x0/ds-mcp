// Package rollout appends Codex-format session rollout lines under $CODEX_HOME/sessions.
package rollout

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recorder appends rollout lines for one session. A nil Recorder is a no-op.
type Recorder struct {
	mu   sync.Mutex
	path string
}

// Open creates the rollout file for threadID. It returns nil when
// DS_MCP_ROLLOUT=off or when the file cannot be created (the error is logged).
func Open(threadID string, created time.Time) *Recorder {
	if os.Getenv("DS_MCP_ROLLOUT") == "off" {
		return nil
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			log.Printf("rollout disabled: resolve home directory: %v", err)
			return nil
		}
		home = filepath.Join(userHome, ".codex")
	}

	created = created.UTC()
	dir := filepath.Join(home, "sessions", created.Format("2006"), created.Format("01"), created.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("rollout disabled: %v", err)
		return nil
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", created.Format("2006-01-02T15-04-05"), threadID))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("rollout disabled: %v", err)
		return nil
	}
	_ = file.Close()
	return &Recorder{path: path}
}

// Path returns the rollout file path, or "" when the recorder is nil or disabled.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Write appends one {timestamp, type, payload} line; the first failure disables the recorder.
// ponytail: Each write reopens the file so sessions hold no descriptors; batch writes if volume matters.
func (r *Recorder) Write(lineType string, payload map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		return
	}

	line, err := json.Marshal(map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      lineType,
		"payload":   payload,
	})
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, err = file.Write(append(line, '\n'))
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		log.Printf("rollout disabled for %s: %v", r.path, err)
		r.path = ""
	}
}

func (r *Recorder) Item(payload map[string]any)  { r.Write("response_item", payload) }
func (r *Recorder) Event(payload map[string]any) { r.Write("event_msg", payload) }
