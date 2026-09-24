//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Available reports whether the kernel supports Landlock.
func Available() error {
	if _, err := llsyscall.LandlockGetABIVersion(); err != nil {
		return fmt.Errorf("landlock unavailable: %w", err)
	}
	return nil
}

// run restricts the current process and execs argv; it only returns on failure.
func run(args []string) error {
	roots, argv, err := parseArgs(args)
	if err != nil {
		return err
	}
	// BestEffort silently becomes a no-op on kernels without Landlock, so require ABI >= 1 first.
	if err := Available(); err != nil {
		return err
	}
	if err := landlock.V10.BestEffort().RestrictPaths(
		landlock.RODirs("/"),
		landlock.RWDirs(roots...),
	); err != nil {
		return fmt.Errorf("landlock unavailable: %w", err)
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
