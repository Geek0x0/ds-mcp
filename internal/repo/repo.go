// Package repo reads repository layout directly from .git files without invoking git.
package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// Root returns the nearest ancestor of dir (inclusive) containing a .git entry.
func Root(dir string) (string, bool) {
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, true
		}
		if filepath.Dir(current) == current {
			return "", false
		}
	}
}

// Head returns the checked-out branch and commit for the repository at root.
// Unknown values are empty strings.
func Head(root string) (branch, commit string) {
	gitDir := filepath.Join(root, ".git")
	if data, err := os.ReadFile(gitDir); err == nil {
		target := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		gitDir = target
	}

	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	ref, isRef := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if !isRef {
		return "", strings.TrimSpace(string(head))
	}
	branch = strings.TrimPrefix(ref, "refs/heads/")

	commonDir := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	}
	for _, dir := range []string{gitDir, commonDir} {
		if data, err := os.ReadFile(filepath.Join(dir, ref)); err == nil {
			return branch, strings.TrimSpace(string(data))
		}
	}
	if data, err := os.ReadFile(filepath.Join(commonDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if hash, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
				return branch, hash
			}
		}
	}
	return branch, ""
}
