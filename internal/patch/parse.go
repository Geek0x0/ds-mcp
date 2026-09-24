// Package patch parses and applies patches in the Codex apply_patch format.
package patch

import (
	"errors"
	"fmt"
	"strings"
)

type Kind int

const (
	Add Kind = iota
	Delete
	Update
)

func (k Kind) String() string {
	switch k {
	case Add:
		return "add"
	case Delete:
		return "delete"
	default:
		return "update"
	}
}

type Chunk struct {
	Header string
	Old    []string
	New    []string
	Raw    []string
	EOF    bool
}

type Hunk struct {
	Kind    Kind
	Path    string
	MoveTo  string
	Content string
	Chunks  []Chunk
}

const (
	beginMarker  = "*** Begin Patch"
	endMarker    = "*** End Patch"
	addPrefix    = "*** Add File: "
	deletePrefix = "*** Delete File: "
	updatePrefix = "*** Update File: "
	movePrefix   = "*** Move to: "
	eofMarker    = "*** End of File"
)

// Parse parses a complete patch. It never touches the filesystem.
func Parse(text string) ([]Hunk, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(text, "\n")
	if len(lines) >= 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "<<") &&
		strings.TrimSpace(lines[len(lines)-1]) == "EOF" {
		lines = lines[1 : len(lines)-1]
	}
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != beginMarker {
		return nil, fmt.Errorf("patch must start with %q", beginMarker)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != endMarker {
		return nil, fmt.Errorf("patch must end with %q", endMarker)
	}

	body := lines[1 : len(lines)-1]
	var hunks []Hunk
	for i := 0; i < len(body); {
		line := body[i]
		switch {
		case strings.HasPrefix(line, addPrefix):
			hunk := Hunk{Kind: Add, Path: strings.TrimSpace(strings.TrimPrefix(line, addPrefix))}
			i++
			var content strings.Builder
			for i < len(body) && strings.HasPrefix(body[i], "+") {
				content.WriteString(body[i][1:])
				content.WriteByte('\n')
				i++
			}
			hunk.Content = content.String()
			hunks = append(hunks, hunk)
		case strings.HasPrefix(line, deletePrefix):
			hunks = append(hunks, Hunk{Kind: Delete, Path: strings.TrimSpace(strings.TrimPrefix(line, deletePrefix))})
			i++
		case strings.HasPrefix(line, updatePrefix):
			hunk := Hunk{Kind: Update, Path: strings.TrimSpace(strings.TrimPrefix(line, updatePrefix))}
			i++
			if i < len(body) && strings.HasPrefix(body[i], movePrefix) {
				hunk.MoveTo = strings.TrimSpace(strings.TrimPrefix(body[i], movePrefix))
				i++
			}
			chunks, next, err := parseChunks(body, i, hunk.Path)
			if err != nil {
				return nil, err
			}
			hunk.Chunks = chunks
			i = next
			hunks = append(hunks, hunk)
		case strings.TrimSpace(line) == "":
			i++
		default:
			return nil, fmt.Errorf("unexpected line %q; expected a file operation header", line)
		}
	}

	if len(hunks) == 0 {
		return nil, errors.New("patch contains no file operations")
	}
	for _, hunk := range hunks {
		if hunk.Path == "" {
			return nil, errors.New("file operation has an empty path")
		}
	}
	return hunks, nil
}

// ponytail: A blank line inside an Update is an empty context line (Codex behavior), so a stray
// blank line before the next header becomes context; strip trailing blank context if models trip on it.
func parseChunks(body []string, i int, path string) ([]Chunk, int, error) {
	var chunks []Chunk
loop:
	for ; i < len(body); i++ {
		line := body[i]
		switch {
		case line == eofMarker:
			if len(chunks) == 0 {
				return nil, 0, fmt.Errorf("%s: %q before any change line", path, eofMarker)
			}
			chunks[len(chunks)-1].EOF = true
		case strings.HasPrefix(line, "*** "):
			break loop
		case line == "@@" || strings.HasPrefix(line, "@@ "):
			chunks = append(chunks, Chunk{Header: strings.TrimSpace(strings.TrimPrefix(line, "@@"))})
		default:
			if len(chunks) == 0 {
				chunks = append(chunks, Chunk{})
			}
			chunk := &chunks[len(chunks)-1]
			if line == "" {
				line = " "
			}
			text := line[1:]
			switch line[0] {
			case ' ':
				chunk.Old = append(chunk.Old, text)
				chunk.New = append(chunk.New, text)
			case '-':
				chunk.Old = append(chunk.Old, text)
			case '+':
				chunk.New = append(chunk.New, text)
			default:
				return nil, 0, fmt.Errorf("%s: invalid patch line %q; lines must start with ' ', '-', or '+'", path, line)
			}
			chunk.Raw = append(chunk.Raw, line)
		}
	}
	if len(chunks) == 0 {
		return nil, 0, fmt.Errorf("%s: update has no changes", path)
	}
	for _, chunk := range chunks {
		if len(chunk.Raw) == 0 {
			return nil, 0, fmt.Errorf("%s: chunk %q has no change lines", path, chunk.Header)
		}
	}
	return chunks, i, nil
}

// Paths returns every path the patch touches, as written, in patch order.
func Paths(hunks []Hunk) []string {
	var paths []string
	for _, hunk := range hunks {
		paths = append(paths, hunk.Path)
		if hunk.MoveTo != "" {
			paths = append(paths, hunk.MoveTo)
		}
	}
	return paths
}
