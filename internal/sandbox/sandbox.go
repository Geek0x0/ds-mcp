// Package sandbox runs commands under a kernel-enforced write restriction by
// re-executing the ds-mcp binary in a hidden helper mode.
package sandbox

import (
	"errors"
	"fmt"
	"os"
)

// HelperArg is the hidden argv[1] that turns the ds-mcp binary into the sandbox helper.
const HelperArg = "__sandbox-exec"

// deviceFiles are always writable under the sandbox; the rest of /dev (including
// /dev/shm and block devices) stays read-only.
var deviceFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty"}

// Command returns the argv that runs argv through the helper at self with only
// writableRoots writable.
func Command(self string, writableRoots []string, argv ...string) []string {
	out := []string{self, HelperArg}
	for _, root := range writableRoots {
		out = append(out, "--rw", root)
	}
	out = append(out, "--")
	return append(out, argv...)
}

// MaybeRunHelper runs the sandbox helper and exits when os.Args selects it;
// otherwise it returns immediately. Call it first in main and in TestMain.
func MaybeRunHelper() {
	if len(os.Args) < 2 || os.Args[1] != HelperArg {
		return
	}
	err := run(os.Args[2:])
	fmt.Fprintf(os.Stderr, "ds-mcp: %v\n", err)
	os.Exit(126)
}

func parseArgs(args []string) ([]string, []string, error) {
	var roots []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rw":
			if i+1 >= len(args) {
				return nil, nil, errors.New("sandbox helper: --rw requires a path")
			}
			roots = append(roots, args[i+1])
			i++
		case "--":
			if i+1 >= len(args) {
				return nil, nil, errors.New("sandbox helper: missing command after --")
			}
			return roots, args[i+1:], nil
		default:
			return nil, nil, fmt.Errorf("sandbox helper: unexpected argument %q", args[i])
		}
	}
	return nil, nil, errors.New("sandbox helper: missing -- separator")
}
