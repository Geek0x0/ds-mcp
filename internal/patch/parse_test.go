package patch

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAllOperations(t *testing.T) {
	text := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: new.txt",
		"+hello",
		"+world",
		"*** Delete File: old.txt",
		"*** Update File: src/a.go",
		"*** Move to: src/b.go",
		"@@ func main() {",
		" \tkeep",
		"-\tgone",
		"+\tadded",
		"",
		"@@",
		"-tail",
		"+TAIL",
		"*** End of File",
		"*** End Patch",
	}, "\n")

	hunks, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []Hunk{
		{Kind: Add, Path: "new.txt", Content: "hello\nworld\n"},
		{Kind: Delete, Path: "old.txt"},
		{Kind: Update, Path: "src/a.go", MoveTo: "src/b.go", Chunks: []Chunk{
			{
				Header: "func main() {",
				Old:    []string{"\tkeep", "\tgone", ""},
				New:    []string{"\tkeep", "\tadded", ""},
				Raw:    []string{" \tkeep", "-\tgone", "+\tadded", " "},
			},
			{
				Old: []string{"tail"},
				New: []string{"TAIL"},
				Raw: []string{"-tail", "+TAIL"},
				EOF: true,
			},
		}},
	}
	if !reflect.DeepEqual(hunks, want) {
		t.Fatalf("Parse() =\n%#v\nwant\n%#v", hunks, want)
	}
	if got := Paths(hunks); !reflect.DeepEqual(got, []string{"new.txt", "old.txt", "src/a.go", "src/b.go"}) {
		t.Fatalf("Paths() = %v", got)
	}
}

func TestParseFirstChunkWithoutHeader(t *testing.T) {
	hunks, err := Parse("*** Begin Patch\n*** Update File: a.txt\n-x\n+y\n*** End Patch")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(hunks[0].Chunks) != 1 || hunks[0].Chunks[0].Old[0] != "x" {
		t.Fatalf("Parse() = %#v", hunks)
	}
}

func TestParseToleratesWrappersAndCRLF(t *testing.T) {
	for name, text := range map[string]string{
		"heredoc":    "<<'EOF'\n*** Begin Patch\n*** Delete File: a.txt\n*** End Patch\nEOF\n",
		"crlf":       "*** Begin Patch\r\n*** Delete File: a.txt\r\n*** End Patch\r\n",
		"whitespace": "\n\n  *** Begin Patch\n*** Delete File: a.txt\n*** End Patch  \n\n",
	} {
		t.Run(name, func(t *testing.T) {
			hunks, err := Parse(text)
			if err != nil || len(hunks) != 1 || hunks[0].Path != "a.txt" {
				t.Fatalf("Parse() = (%#v, %v)", hunks, err)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for name, text := range map[string]string{
		"missing begin":     "*** Delete File: a\n*** End Patch",
		"missing end":       "*** Begin Patch\n*** Delete File: a",
		"empty":             "*** Begin Patch\n*** End Patch",
		"unknown header":    "*** Begin Patch\n*** Rename File: a\n*** End Patch",
		"bad chunk line":    "*** Begin Patch\n*** Update File: a\n@@\n?x\n*** End Patch",
		"update no changes": "*** Begin Patch\n*** Update File: a\n*** End Patch",
		"empty path":        "*** Begin Patch\n*** Delete File: \n*** End Patch",
		"eof first":         "*** Begin Patch\n*** Update File: a\n*** End of File\n*** End Patch",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); err == nil {
				t.Fatalf("Parse() error = nil, want error")
			}
		})
	}
}
