package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func applyText(t *testing.T, cwd, text string) ([]Change, error) {
	t.Helper()
	hunks, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	changes, err := Plan(cwd, hunks)
	if err != nil {
		return nil, err
	}
	return changes, Commit(changes)
}

func TestApplyUpdateAddDeleteMove(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "a.txt"), "one\ntwo\nthree\n")
	write(t, filepath.Join(cwd, "gone.txt"), "bye\n")
	write(t, filepath.Join(cwd, "m.txt"), "move me\n")

	changes, err := applyText(t, cwd, strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		" one",
		"-two",
		"+TWO",
		" three",
		"*** Add File: dir/new.txt",
		"+fresh",
		"*** Delete File: gone.txt",
		"*** Update File: m.txt",
		"*** Move to: moved/m.txt",
		"-move me",
		"+moved",
		"*** End Patch",
	}, "\n"))
	if err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := read(t, filepath.Join(cwd, "a.txt")); got != "one\nTWO\nthree\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := read(t, filepath.Join(cwd, "dir", "new.txt")); got != "fresh\n" {
		t.Errorf("new.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("gone.txt still exists")
	}
	if _, err := os.Stat(filepath.Join(cwd, "m.txt")); !os.IsNotExist(err) {
		t.Errorf("m.txt still exists after move")
	}
	if got := read(t, filepath.Join(cwd, "moved", "m.txt")); got != "moved\n" {
		t.Errorf("moved/m.txt = %q", got)
	}
	if changes[0].Diff != "@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n" {
		t.Errorf("diff = %q", changes[0].Diff)
	}
	summary := Summary(changes)
	for _, want := range []string{
		"Success. Updated the following files:",
		"M " + filepath.Join(cwd, "a.txt"),
		"A " + filepath.Join(cwd, "dir", "new.txt"),
		"D " + filepath.Join(cwd, "gone.txt"),
		"M " + filepath.Join(cwd, "moved", "m.txt"),
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q lacks %q", summary, want)
		}
	}
}

func TestApplyFuzzyWhitespaceAndHeader(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "f.go"), "func a() {\n\tx := 1   \n}\nfunc b() {\n\tx := 1\n}\n")

	_, err := applyText(t, cwd, "*** Begin Patch\n*** Update File: f.go\n@@ func b() {\n-\tx := 1\n+\tx := 2\n*** End Patch")
	if err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := read(t, filepath.Join(cwd, "f.go")); got != "func a() {\n\tx := 1   \n}\nfunc b() {\n\tx := 2\n}\n" {
		t.Errorf("header did not scope the match: %q", got)
	}

	_, err = applyText(t, cwd, "*** Begin Patch\n*** Update File: f.go\n-  x := 1\n+\tx := 9\n*** End Patch")
	if err != nil {
		t.Fatalf("trim-insensitive apply error = %v", err)
	}
	if got := read(t, filepath.Join(cwd, "f.go")); !strings.HasPrefix(got, "func a() {\n\tx := 9\n") {
		t.Errorf("trim-insensitive match failed: %q", got)
	}
}

func TestApplyEndOfFileAnchor(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "f.txt"), "x\ny\nx\n")
	if _, err := applyText(t, cwd, "*** Begin Patch\n*** Update File: f.txt\n-x\n+z\n*** End of File\n*** End Patch"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := read(t, filepath.Join(cwd, "f.txt")); got != "x\ny\nz\n" {
		t.Errorf("f.txt = %q", got)
	}
}

func TestApplyPreservesMissingTrailingNewline(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "f.txt"), "a\nb")
	if _, err := applyText(t, cwd, "*** Begin Patch\n*** Update File: f.txt\n-b\n+c\n*** End Patch"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := read(t, filepath.Join(cwd, "f.txt")); got != "a\nc" {
		t.Errorf("f.txt = %q", got)
	}
}

func TestPlanFailureWritesNothing(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "a.txt"), "one\n")
	write(t, filepath.Join(cwd, "exists.txt"), "here\n")

	for name, text := range map[string]string{
		"chunk not found": "*** Begin Patch\n*** Add File: new.txt\n+x\n*** Update File: a.txt\n-missing\n+y\n*** End Patch",
		"add existing":    "*** Begin Patch\n*** Add File: exists.txt\n+x\n*** End Patch",
		"update missing":  "*** Begin Patch\n*** Update File: nope.txt\n-x\n+y\n*** End Patch",
		"delete missing":  "*** Begin Patch\n*** Delete File: nope.txt\n*** End Patch",
	} {
		t.Run(name, func(t *testing.T) {
			hunks, err := Parse(text)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if _, err := Plan(cwd, hunks); err == nil {
				t.Fatalf("Plan() error = nil, want error")
			}
			if _, err := os.Stat(filepath.Join(cwd, "new.txt")); !os.IsNotExist(err) {
				t.Fatalf("new.txt written despite plan failure")
			}
			if got := read(t, filepath.Join(cwd, "a.txt")); got != "one\n" {
				t.Fatalf("a.txt modified: %q", got)
			}
		})
	}
}
