package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Change struct {
	Kind       Kind
	Path       string
	MoveTo     string
	NewContent string
	Diff       string
}

// Plan computes every resulting file in memory; it writes nothing.
// ponytail: Each hunk reads its file from disk, so two hunks on the same file in one
// patch do not see each other's edits; thread an in-memory overlay through Plan if needed.
func Plan(cwd string, hunks []Hunk) ([]Change, error) {
	changes := make([]Change, 0, len(hunks))
	for _, hunk := range hunks {
		path := resolve(cwd, hunk.Path)
		switch hunk.Kind {
		case Add:
			if _, err := os.Lstat(path); err == nil {
				return nil, fmt.Errorf("add %s: file already exists", hunk.Path)
			}
			changes = append(changes, Change{Kind: Add, Path: path, NewContent: hunk.Content, Diff: hunk.Content})
		case Delete:
			info, err := os.Stat(path)
			if err != nil {
				return nil, fmt.Errorf("delete %s: %w", hunk.Path, err)
			}
			if info.IsDir() {
				return nil, fmt.Errorf("delete %s: is a directory", hunk.Path)
			}
			changes = append(changes, Change{Kind: Delete, Path: path})
		case Update:
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("update %s: %w", hunk.Path, err)
			}
			content, diff, err := applyChunks(string(data), hunk.Chunks)
			if err != nil {
				return nil, fmt.Errorf("update %s: %w", hunk.Path, err)
			}
			change := Change{Kind: Update, Path: path, NewContent: content, Diff: diff}
			if hunk.MoveTo != "" {
				change.MoveTo = resolve(cwd, hunk.MoveTo)
			}
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func resolve(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

type replacement struct {
	start  int
	oldLen int
	lines  []string
	raw    []string
}

func applyChunks(original string, chunks []Chunk) (string, string, error) {
	trailingNewline := original == "" || strings.HasSuffix(original, "\n")
	var lines []string
	if original != "" {
		lines = strings.Split(strings.TrimSuffix(original, "\n"), "\n")
	}

	var replacements []replacement
	position := 0
	for _, chunk := range chunks {
		if chunk.Header != "" {
			at := seek(lines, []string{chunk.Header}, position, false)
			if at < 0 {
				return "", "", fmt.Errorf("context header %q not found", chunk.Header)
			}
			position = at + 1
		}
		if len(chunk.Old) == 0 {
			at := len(lines)
			if chunk.Header != "" {
				at = position
			}
			replacements = append(replacements, replacement{start: at, lines: chunk.New, raw: chunk.Raw})
			position = at
			continue
		}
		at := seek(lines, chunk.Old, position, chunk.EOF)
		if at < 0 {
			return "", "", fmt.Errorf("could not find chunk starting with %q", chunk.Old[0])
		}
		replacements = append(replacements, replacement{start: at, oldLen: len(chunk.Old), lines: chunk.New, raw: chunk.Raw})
		position = at + len(chunk.Old)
	}

	var out []string
	var diff strings.Builder
	previous, offset := 0, 0
	for _, r := range replacements {
		out = append(out, lines[previous:r.start]...)
		out = append(out, r.lines...)
		fmt.Fprintf(&diff, "@@ -%d,%d +%d,%d @@\n", r.start+1, r.oldLen, r.start+1+offset, len(r.lines))
		for _, line := range r.raw {
			diff.WriteString(line)
			diff.WriteByte('\n')
		}
		offset += len(r.lines) - r.oldLen
		previous = r.start + r.oldLen
	}
	out = append(out, lines[previous:]...)

	result := strings.Join(out, "\n")
	if trailingNewline && len(out) > 0 {
		result += "\n"
	}
	return result, diff.String(), nil
}

var normalizers = []func(string) string{
	func(s string) string { return s },
	func(s string) string { return strings.TrimRight(s, " \t") },
	strings.TrimSpace,
}

// seek returns the first index at or after start where pattern matches, trying
// exact, trailing-whitespace-insensitive, then surrounding-whitespace-insensitive
// comparison; eof anchors the match at the end of lines.
func seek(lines, pattern []string, start int, eof bool) int {
	for _, normalize := range normalizers {
		if eof {
			at := len(lines) - len(pattern)
			if at >= start && matchAt(lines, pattern, at, normalize) {
				return at
			}
			continue
		}
		for at := start; at+len(pattern) <= len(lines); at++ {
			if matchAt(lines, pattern, at, normalize) {
				return at
			}
		}
	}
	return -1
}

func matchAt(lines, pattern []string, at int, normalize func(string) string) bool {
	for i, want := range pattern {
		if normalize(lines[at+i]) != normalize(want) {
			return false
		}
	}
	return true
}

// Commit writes planned changes in order.
// ponytail: Moved files are recreated with mode 0644; carry the source mode through Change if exec bits matter.
func Commit(changes []Change) error {
	for _, change := range changes {
		switch change.Kind {
		case Add:
			if err := writeFile(change.Path, change.NewContent); err != nil {
				return err
			}
		case Delete:
			if err := os.Remove(change.Path); err != nil {
				return err
			}
		case Update:
			target := change.Path
			if change.MoveTo != "" {
				target = change.MoveTo
			}
			if err := writeFile(target, change.NewContent); err != nil {
				return err
			}
			if change.MoveTo != "" && change.MoveTo != change.Path {
				if err := os.Remove(change.Path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Summary renders the Codex-style success message.
func Summary(changes []Change) string {
	var out strings.Builder
	out.WriteString("Success. Updated the following files:")
	for _, change := range changes {
		letter, path := "M", change.Path
		switch change.Kind {
		case Add:
			letter = "A"
		case Delete:
			letter = "D"
		}
		if change.MoveTo != "" {
			path = change.MoveTo
		}
		fmt.Fprintf(&out, "\n%s %s", letter, path)
	}
	return out.String()
}
