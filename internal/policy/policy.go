package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sandbox controls which operations can run without leaving the configured sandbox.
type Sandbox string

// ApprovalPolicy controls when operations outside the sandbox require approval.
type ApprovalPolicy string

// Decision is the action the caller should take for a request.
type Decision int

const (
	Allow Decision = iota
	AskApproval
	Deny
)

const (
	sandboxReadOnly         Sandbox = "read-only"
	sandboxWorkspaceWrite   Sandbox = "workspace-write"
	sandboxDangerFullAccess Sandbox = "danger-full-access"
)

const (
	approvalUntrusted ApprovalPolicy = "untrusted"
	approvalOnRequest ApprovalPolicy = "on-request"
	approvalOnFailure ApprovalPolicy = "on-failure"
	approvalNever     ApprovalPolicy = "never"
)

// Request describes an operation to evaluate.
type Request struct {
	Tool    string
	Command string
	Path    string
	Cwd     string
}

var simpleAllowed = map[string]bool{
	"ls":    true,
	"cat":   true,
	"head":  true,
	"tail":  true,
	"rg":    true,
	"grep":  true,
	"find":  true,
	"pwd":   true,
	"wc":    true,
	"stat":  true,
	"which": true,
	"echo":  true,
}

var gitAllowed = map[string]bool{
	"status":    true,
	"diff":      true,
	"log":       true,
	"show":      true,
	"branch":    true,
	"blame":     true,
	"rev-parse": true,
	"ls-files":  true,
}

// ParseSandbox validates a sandbox value.
func ParseSandbox(s string) (Sandbox, error) {
	sandbox := Sandbox(s)
	switch sandbox {
	case sandboxReadOnly, sandboxWorkspaceWrite, sandboxDangerFullAccess:
		return sandbox, nil
	default:
		return "", fmt.Errorf("invalid sandbox %q", s)
	}
}

// ParseApprovalPolicy validates an approval policy value.
func ParseApprovalPolicy(s string) (ApprovalPolicy, error) {
	policy := ApprovalPolicy(s)
	switch policy {
	case approvalUntrusted, approvalOnRequest, approvalOnFailure, approvalNever:
		return policy, nil
	default:
		return "", fmt.Errorf("invalid approval policy %q", s)
	}
}

// Evaluate combines sandbox and approval policy rules for a request.
func Evaluate(sb Sandbox, ap ApprovalPolicy, req Request) (Decision, string) {
	trusted := req.Tool == "read_file" || (req.Tool == "shell" && isWhitelistedShell(req.Command))
	inSandbox, reason := withinSandbox(sb, req)

	switch ap {
	case approvalUntrusted:
		if trusted {
			return Allow, ""
		}
		if reason == "" {
			reason = "untrusted policy requires approval for non-whitelisted operations"
		}
		return AskApproval, reason
	case approvalNever:
		if inSandbox {
			return Allow, ""
		}
		return Deny, reason
	default:
		if inSandbox {
			return Allow, ""
		}
		return AskApproval, reason
	}
}

func isWhitelistedShell(command string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	for _, metacharacter := range []string{">", "`", "$(", "<("} {
		if strings.Contains(command, metacharacter) {
			return false
		}
	}

	// ponytail: Quoted metacharacters such as find -name "a;b" are known false negatives that fall through to approval.
	segments := strings.FieldsFunc(command, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n'
	})
	matchedSegment := false
	for _, segment := range segments {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		matchedSegment = true
		if simpleAllowed[fields[0]] {
			continue
		}
		if fields[0] == "git" && len(fields) > 1 && gitAllowed[fields[1]] {
			continue
		}
		return false
	}

	return matchedSegment
}

func pathInside(cwd, target string) (bool, string) {
	cwdAbsolute, err := filepath.Abs(cwd)
	if err != nil {
		return false, fmt.Sprintf("resolve working directory %q: %v", cwd, err)
	}
	resolvedCwd, err := filepath.EvalSymlinks(cwdAbsolute)
	if err != nil {
		return false, fmt.Sprintf("resolve working directory %q: %v", cwd, err)
	}

	if !filepath.IsAbs(target) {
		target = filepath.Join(cwdAbsolute, target)
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return false, fmt.Sprintf("resolve target path %q: %v", target, err)
	}

	ancestor := target
	var missingParts []string
	for {
		_, statErr := os.Lstat(ancestor)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) {
			return false, fmt.Sprintf("inspect target ancestor %q: %v", ancestor, statErr)
		}

		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return false, fmt.Sprintf("no existing ancestor for target %q", target)
		}
		missingParts = append(missingParts, filepath.Base(ancestor))
		ancestor = parent
	}

	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return false, fmt.Sprintf("resolve target ancestor %q: %v", ancestor, err)
	}
	resolvedTarget := resolvedAncestor
	for i := len(missingParts) - 1; i >= 0; i-- {
		resolvedTarget = filepath.Join(resolvedTarget, missingParts[i])
	}

	relative, err := filepath.Rel(resolvedCwd, resolvedTarget)
	if err != nil {
		return false, fmt.Sprintf("compare target %q with working directory %q: %v", resolvedTarget, resolvedCwd, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, fmt.Sprintf("target %q resolves outside working directory %q", target, resolvedCwd)
	}

	return true, ""
}

func withinSandbox(sb Sandbox, req Request) (bool, string) {
	switch sb {
	case sandboxReadOnly:
		if req.Tool == "read_file" || (req.Tool == "shell" && isWhitelistedShell(req.Command)) {
			return true, ""
		}
		return false, "operation is not permitted by the read-only sandbox"
	case sandboxWorkspaceWrite:
		switch req.Tool {
		case "read_file", "shell":
			return true, ""
		case "write_file":
			return pathInside(req.Cwd, req.Path)
		default:
			return false, fmt.Sprintf("tool %q is not permitted by the workspace-write sandbox", req.Tool)
		}
	case sandboxDangerFullAccess:
		return true, ""
	default:
		return false, fmt.Sprintf("unknown sandbox %q", sb)
	}
}
