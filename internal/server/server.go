package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Geek0x0/ds-mcp/internal/agent"
	"github.com/Geek0x0/ds-mcp/internal/policy"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const maxConfiguredTurns = 100000

var reasoningEfforts = map[string]string{
	"low":    "low",
	"medium": "high",
	"high":   "high",
	"xhigh":  "max",
	"max":    "max",
}

const reasoningEffortValues = "low, medium, high, xhigh, max"

// resolveReasoningEffort returns the requested effort value and the value sent
// to the API. The top-level argument wins over config.model_reasoning_effort.
func resolveReasoningEffort(arguments map[string]any, config map[string]any) (string, string, error) {
	requested := "high"
	if raw, present := config["model_reasoning_effort"]; present {
		value, ok := raw.(string)
		if !ok {
			return "", "", fmt.Errorf("config.model_reasoning_effort must be a string; valid values: %s", reasoningEffortValues)
		}
		if _, known := reasoningEfforts[value]; !known {
			return "", "", fmt.Errorf("invalid config.model_reasoning_effort %q; valid values: %s", value, reasoningEffortValues)
		}
		requested = value
	}
	if raw, present := arguments["reasoning-effort"]; present {
		value, ok := raw.(string)
		if !ok {
			return "", "", fmt.Errorf("reasoning-effort must be a string; valid values: %s", reasoningEffortValues)
		}
		if value != "" {
			if _, known := reasoningEfforts[value]; !known {
				return "", "", fmt.Errorf("invalid reasoning-effort %q; valid values: %s", value, reasoningEffortValues)
			}
			requested = value
		}
	}
	return requested, reasoningEfforts[requested], nil
}

func parseWritableRoots(config map[string]any) ([]string, error) {
	raw, present := config["writable_roots"]
	if !present {
		return nil, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, errors.New("config.writable_roots must be an array of absolute directory paths")
	}
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		path, ok := entry.(string)
		if !ok || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("config.writable_roots entry %v must be an absolute path", entry)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("config.writable_roots entry %q must be an existing directory", path)
		}
		roots = append(roots, filepath.Clean(path))
	}
	return roots, nil
}

type Server struct {
	mcp    *mcpserver.MCPServer
	mgr    *agent.Manager
	runner *agent.Runner
}

func New(client agent.ChatClient, version string) *Server {
	s := &Server{mgr: agent.NewManager()}
	s.mcp = mcpserver.NewMCPServer(
		"ds-mcp",
		version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithRecovery(),
	)
	s.runner = &agent.Runner{Client: client, Emitter: s, Approver: s}
	s.mcp.AddTool(deepseekTool(), s.handleDeepseek)
	s.mcp.AddTool(replyTool(), s.handleReply)
	return s
}

func (s *Server) ServeStdio() error {
	return mcpserver.ServeStdio(s.mcp)
}

type toolOutput struct {
	ThreadID string `json:"threadId" jsonschema_description:"ID of the DeepSeek agent thread that produced this result."`
	Content  string `json:"content" jsonschema_description:"Human-readable report text from the DeepSeek agent."`
}

func deepseekTool() mcp.Tool {
	return mcp.NewTool(
		"deepseek",
		mcp.WithDescription("Start a new DeepSeek coding-agent thread."),
		mcp.WithString(
			"prompt",
			mcp.Required(),
			mcp.Description("Task prompt to send to the new DeepSeek agent thread."),
		),
		mcp.WithString(
			"model",
			mcp.Description("DeepSeek model name; defaults to deepseek-v4-pro."),
		),
		mcp.WithString(
			"cwd",
			mcp.Description("Absolute path to an existing working directory; defaults to the ds-mcp process working directory."),
		),
		mcp.WithString(
			"sandbox",
			mcp.Description("Sandbox mode: read-only, workspace-write, or danger-full-access; defaults to read-only."),
		),
		mcp.WithString(
			"approval-policy",
			mcp.Description("Approval policy: untrusted, on-request, on-failure, or never; defaults to on-request."),
		),
		mcp.WithString(
			"reasoning-effort",
			mcp.Description("Reasoning effort for DeepSeek's thinking mode: low, medium, high, xhigh, or max (medium maps to high, xhigh maps to max); defaults to high, or to config.model_reasoning_effort when set."),
		),
		mcp.WithString(
			"base-instructions",
			mcp.Description("Complete replacement for the built-in base system instructions; empty or omitted uses the built-in default."),
		),
		mcp.WithString(
			"developer-instructions",
			mcp.Description("Additional system instructions appended after the selected base instructions; empty or omitted appends nothing."),
		),
		mcp.WithObject(
			"config",
			mcp.Description("loose config map; recognized keys: max_turns (number from 1 to 100000), model_reasoning_effort (same values as reasoning-effort; the top-level argument wins), writable_roots (array of absolute directory paths the shell may also write under workspace-write); other unknown keys are silently ignored"),
		),
		mcp.WithOutputSchema[toolOutput](),
	)
}

func replyTool() mcp.Tool {
	return mcp.NewTool(
		"deepseek-reply",
		mcp.WithDescription("Continue an existing DeepSeek coding-agent thread."),
		mcp.WithString(
			"threadId",
			mcp.Required(),
			mcp.Description("Thread ID returned by a previous deepseek or deepseek-reply call."),
		),
		mcp.WithString(
			"prompt",
			mcp.Required(),
			mcp.Description("Follow-up prompt to send to the existing DeepSeek agent thread."),
		),
		mcp.WithOutputSchema[toolOutput](),
	)
}

func (s *Server) handleDeepseek(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError("prompt is required: " + err.Error()), nil
	}
	arguments := req.GetArguments()

	sandboxValue := req.GetString("sandbox", "read-only")
	sandbox, err := policy.ParseSandbox(sandboxValue)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"invalid sandbox %q; valid values: read-only, workspace-write, danger-full-access",
			sandboxValue,
		)), nil
	}

	approvalValue := req.GetString("approval-policy", "on-request")
	approval, err := policy.ParseApprovalPolicy(approvalValue)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"invalid approval-policy %q; valid values: untrusted, on-request, on-failure, never",
			approvalValue,
		)), nil
	}

		config, _ := arguments["config"].(map[string]any)
		_, reasoningEffort, err := resolveReasoningEffort(arguments, config)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

	var cwd string
	if raw, present := arguments["cwd"]; present {
		var ok bool
		cwd, ok = raw.(string)
		if !ok {
			return mcp.NewToolResultError(`argument "cwd" must be a string`), nil
		}
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return mcp.NewToolResultError("resolve default cwd: " + err.Error()), nil
		}
	}
	if !filepath.IsAbs(cwd) {
		return mcp.NewToolResultError(fmt.Sprintf("cwd must be an absolute path: %q", cwd)), nil
	}
	cwdInfo, err := os.Stat(cwd)
	if err != nil {
		if os.IsNotExist(err) {
			return mcp.NewToolResultError(fmt.Sprintf("cwd does not exist: %q", cwd)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("inspect cwd %q: %v", cwd, err)), nil
	}
	if !cwdInfo.IsDir() {
		return mcp.NewToolResultError(fmt.Sprintf("cwd is not a directory: %q", cwd)), nil
	}

	systemPrompt := agent.DefaultSystemPrompt
	if baseInstructions := req.GetString("base-instructions", ""); baseInstructions != "" {
		systemPrompt = baseInstructions
	}
	agentsMD, err := agent.LoadAgentsMD(cwd)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if agentsMD != "" {
		systemPrompt += "\n\n" + agentsMD
	}
	if developerInstructions := req.GetString("developer-instructions", ""); developerInstructions != "" {
		systemPrompt += "\n\n" + developerInstructions
	}

	maxTurns := 0
	if value, ok := config["max_turns"].(float64); ok && value > 0 && value <= maxConfiguredTurns {
		maxTurns = int(value)
	}

	writableRoots, err := parseWritableRoots(config)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	sess := s.mgr.Create(agent.Options{
		Model:           req.GetString("model", ""),
		Cwd:             cwd,
		Sandbox:         sandbox,
		Approval:        approval,
		ReasoningEffort: reasoningEffort,
		SystemPrompt:    systemPrompt,
		MaxTurns:        maxTurns,
		WritableRoots:   writableRoots,
	})
	text, err := s.runner.Run(ctx, sess, prompt)
	return resultWithThreadID(sess.ID, text, err), nil
}

func (s *Server) handleReply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	threadID, err := req.RequireString("threadId")
	if err != nil {
		return mcp.NewToolResultError("threadId is required: " + err.Error()), nil
	}
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError("prompt is required: " + err.Error()), nil
	}

	sess, ok := s.mgr.Get(threadID)
	if !ok {
		return mcp.NewToolResultError("unknown threadId: " + threadID), nil
	}

	text, err := s.runner.Run(ctx, sess, prompt)
	if errors.Is(err, agent.ErrBusy) {
		return resultWithThreadID(
			threadID,
			"",
			fmt.Errorf("thread %s is busy with another call", threadID),
		), nil
	}
	return resultWithThreadID(threadID, text, err), nil
}

func resultWithThreadID(threadID, text string, err error) *mcp.CallToolResult {
	if err == nil {
		structured := map[string]any{"threadId": threadID, "content": text}
		return mcp.NewToolResultStructured(structured, text)
	}

	errorText := "error: " + err.Error()
	structured := map[string]any{"threadId": threadID, "content": errorText}
	result := mcp.NewToolResultStructured(structured, errorText)
	result.IsError = true
	return result
}

func (s *Server) Emit(ctx context.Context, threadID string, msg map[string]any) {
	if err := s.mcp.SendNotificationToClient(ctx, "deepseek/event", map[string]any{
		"threadId": threadID,
		"msg":      msg,
	}); err != nil {
		log.Printf("deepseek/event emit failed: %v", err)
	}
}

func (s *Server) Approve(ctx context.Context, threadID string, req agent.ApprovalRequest) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	target := req.Command
	if target == "" {
		target = req.Path
	}
	result, err := s.mcp.RequestElicitation(ctx, mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message: fmt.Sprintf(
				"ds-mcp approval request (thread %s)\ntool: %s\ntarget: %s\nreason: %s",
				threadID,
				req.Tool,
				target,
				req.Reason,
			),
			RequestedSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	})
	if err != nil {
		log.Printf("elicitation unavailable, denying: %v", err)
		return false
	}
	return result.Action == mcp.ElicitationResponseActionAccept
}
