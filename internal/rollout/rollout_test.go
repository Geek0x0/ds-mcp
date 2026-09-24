package rollout

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenLayoutAndWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("SUBAGENT_MCP_ROLLOUT", "")
	created := time.Date(2026, 9, 23, 23, 56, 7, 0, time.FixedZone("PDT", -7*3600))

	r := Open("thread-1", created)
	if r == nil {
		t.Fatalf("Open() = nil")
	}
	wantPath := filepath.Join(home, "sessions", "2026", "09", "24", "rollout-2026-09-24T06-56-07-thread-1.jsonl")
	if r.Path() != wantPath {
		t.Fatalf("Path() = %q, want %q", r.Path(), wantPath)
	}

	r.Write("session_meta", map[string]any{"id": "thread-1"})
	r.Item(map[string]any{"type": "message"})
	r.Event(map[string]any{"type": "task_started"})

	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(wantPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirInfo.Mode().Perm())
	}

	file, err := os.Open(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var types []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line struct {
			Timestamp string         `json:"timestamp"`
			Type      string         `json:"type"`
			Payload   map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("invalid JSON line %q: %v", scanner.Text(), err)
		}
		if _, err := time.Parse(time.RFC3339Nano, line.Timestamp); err != nil || line.Payload == nil {
			t.Fatalf("bad line %q", scanner.Text())
		}
		types = append(types, line.Type)
	}
	if len(types) != 3 || types[0] != "session_meta" || types[1] != "response_item" || types[2] != "event_msg" {
		t.Fatalf("types = %v", types)
	}
}

func TestOpenDisabled(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("SUBAGENT_MCP_ROLLOUT", "off")
	if r := Open("t", time.Now()); r != nil {
		t.Fatalf("Open() = %#v, want nil", r)
	}
}

func TestOpenUnwritableHomeReturnsNil(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", file)
	t.Setenv("SUBAGENT_MCP_ROLLOUT", "")
	if r := Open("t", time.Now()); r != nil {
		t.Fatalf("Open() = %#v, want nil", r)
	}
}

func TestNilRecorderIsNoop(t *testing.T) {
	var r *Recorder
	r.Write("x", map[string]any{})
	r.Item(map[string]any{})
	r.Event(map[string]any{})
	if r.Path() != "" {
		t.Fatalf("nil Path() = %q", r.Path())
	}
}

func TestWriteFailureDisablesRecorder(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("SUBAGENT_MCP_ROLLOUT", "")
	r := Open("t", time.Now())
	if err := os.Remove(r.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.Path(), 0o700); err != nil { // appending to a directory fails
		t.Fatal(err)
	}
	r.Write("a", map[string]any{})
	r.Write("b", map[string]any{})
	if r.Path() != "" {
		t.Fatalf("recorder still active after write failure")
	}
}
