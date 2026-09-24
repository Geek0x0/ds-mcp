package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Geek0x0/ds-mcp/internal/repo"
)

const agentsMDMaxBytes = 32 * 1024

// LoadAgentsMD returns the AGENTS.md files from the repository root down to cwd,
// each wrapped in an <agents_md> block, capped at agentsMDMaxBytes of content.
func LoadAgentsMD(cwd string) (string, error) {
	dir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd for AGENTS.md: %w", err)
	}
	root, ok := repo.Root(dir)
	if !ok {
		root = dir
	}
	var dirs []string
	for current := dir; ; current = filepath.Dir(current) {
		dirs = append(dirs, current)
		if current == root || filepath.Dir(current) == current {
			break
		}
	}

	var out strings.Builder
	remaining := agentsMDMaxBytes
	// ponytail: Files after an exact-fit cap are skipped without a note; add one if exact fits show up in practice.
	for i := len(dirs) - 1; i >= 0 && remaining > 0; i-- {
		path := filepath.Join(dirs[i], "AGENTS.md")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		content := string(data)
		truncated := len(content) > remaining
		if truncated {
			content = content[:remaining]
			// Back off only a rune split by the cut; invalid bytes earlier in the file are kept as-is.
			for i := 0; i < utf8.UTFMax-1 && len(content) > 0; i++ {
				if r, size := utf8.DecodeLastRuneInString(content); r != utf8.RuneError || size != 1 {
					break
				}
				content = content[:len(content)-1]
			}
		}
		remaining -= len(content)
		if truncated {
			remaining = 0
		}

		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		fmt.Fprintf(&out, "<agents_md path=%q>\n%s\n", path, content)
		if truncated {
			fmt.Fprintf(&out, "[AGENTS.md content truncated at %d bytes]\n", agentsMDMaxBytes)
		}
		out.WriteString("</agents_md>")
	}
	return out.String(), nil
}
