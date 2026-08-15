package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"ds-mcp/internal/agent"
	"ds-mcp/internal/policy"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type Server struct {
	mcp    *mcpserver.MCPServer
	mgr    *agent.Manager
	runner *agent.Runner
}

func New(client agent.ChatClient, version string) *Server {
	s := &Server{mgr: agent.NewManager()}
	s.mcp = mcpserver.NewMCPServer("ds-mcp", version, mcpserver.WithToolCapabilities(false))
	s.runner = &agent.Runner{Client: client, Emitter: s, Approver: s}
	s.mcp.AddTool(deepseekTool(), s.handleDeepseek)
	s.mcp.AddTool(replyTool(), s.handleReply)
	return s
}

func (s *Server) ServeStdio() error {
	return mcpserver.ServeStdio(s.mcp)
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
			mcp.Description("DeepSeek model name; defaults to deepseek-chat."),
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
			"base-instructions",
			mcp.Description("Complete replacement for the built-in base system instructions; empty or omitted uses the built-in default."),
		),
		mcp.WithString(
			"developer-instructions",
			mcp.Description("Additional system instructions appended after the selected base instructions; empty or omitted appends nothing."),
		),
		mcp.WithObject(
			"config",
			mcp.Description("loose config map; recognized key: max_turns (number); unknown keys are silently ignored"),
		),
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
	)
}

func (s *Server) handleDeepseek(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError("prompt is required: " + err.Error()), nil
	}

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

	cwd := req.GetString("cwd", "")
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
	if developerInstructions := req.GetString("developer-instructions", ""); developerInstructions != "" {
		systemPrompt += "\n\n" + developerInstructions
	}

	maxTurns := 0
	if config, ok := req.GetArguments()["config"].(map[string]any); ok {
		if value, ok := config["max_turns"].(float64); ok && value > 0 {
			maxTurns = int(value)
		}
	}

	sess := s.mgr.Create(agent.Options{
		Model:        req.GetString("model", ""),
		Cwd:          cwd,
		Sandbox:      sandbox,
		Approval:     approval,
		SystemPrompt: systemPrompt,
		MaxTurns:     maxTurns,
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
		return mcp.NewToolResultError("thread " + threadID + " is busy with another call"), nil
	}
	return resultWithThreadID(threadID, text, err), nil
}

func resultWithThreadID(threadID, text string, err error) *mcp.CallToolResult {
	structured := map[string]any{"threadId": threadID}
	if err == nil {
		return mcp.NewToolResultStructured(structured, text)
	}

	result := mcp.NewToolResultStructured(structured, "error: "+err.Error())
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
