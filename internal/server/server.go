package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Geek0x0/subagent-mcp/internal/agent"
	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/policy"
	"github.com/Geek0x0/subagent-mcp/internal/provider"
	"github.com/Geek0x0/subagent-mcp/internal/repo"
	"github.com/Geek0x0/subagent-mcp/internal/rollout"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const (
	maxConfiguredTurns = 100000

	// defaultToolName is the base name of the two MCP tools when no
	// WithToolName option is supplied.
	defaultToolName = "subagent"

	// requestIDMetaKey is the _meta field the before-call-tool hook uses to hand
	// the JSON-RPC request ID to the tool handlers, which cannot see it otherwise.
	requestIDMetaKey = "subagent-mcp/requestId"

	// cancelledNotificationMethod is the MCP notification clients send when they
	// abandon an in-flight request, for example when the user presses Esc.
	cancelledNotificationMethod = "notifications/cancelled"

	// progressSummaryLimit is the maximum number of runes a
	// notifications/progress message carries before it is truncated.
	progressSummaryLimit = 200
)

// progressContextKey keys the per-call MCP progress state in a tool-call
// context. The state is only attached when the request opted in with a
// progress token.
type progressContextKey struct{}

// progressState tracks how many progress notifications a call has sent.
type progressState struct {
	token mcp.ProgressToken

	mu    sync.Mutex
	count int
}

func (p *progressState) next() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	return float64(p.count)
}

// canonicalRequestID returns a stable map key for a JSON-RPC request ID so the
// before-call-tool hook and notifications/cancelled agree on the same value for
// both numeric and string IDs. JSON numbers decode as float64 in both places,
// so integral floats are keyed as integers.
func canonicalRequestID(value any) (string, bool) {
	switch v := value.(type) {
	case mcp.RequestId:
		return canonicalRequestID(v.Value())
	case string:
		return "string:" + v, true
	case float64:
		if v == float64(int64(v)) {
			return "int64:" + strconv.FormatInt(int64(v), 10), true
		}
		return "float64:" + strconv.FormatFloat(v, 'f', -1, 64), true
	case int64:
		return "int64:" + strconv.FormatInt(v, 10), true
	case int:
		return "int64:" + strconv.FormatInt(int64(v), 10), true
	default:
		return "", false
	}
}

// requestIDFromMeta returns the canonical request key stored by the
// before-call-tool hook, or false when the request has none.
func requestIDFromMeta(req mcp.CallToolRequest) (string, bool) {
	if req.Params.Meta == nil {
		return "", false
	}
	key, ok := req.Params.Meta.AdditionalFields[requestIDMetaKey].(string)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

// effortValuesText lists the accepted reasoning effort values for error messages.
func effortValuesText() string {
	return strings.Join(config.EffortValues, ", ")
}

// resolveEffort returns the requested effort value. The top-level argument wins
// over config.model_reasoning_effort; each source is validated against the
// accepted effort values.
func resolveEffort(arguments map[string]any, configMap map[string]any) (string, error) {
	requested := "high"
	if raw, present := configMap["model_reasoning_effort"]; present {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("config.model_reasoning_effort must be a string; valid values: %s", effortValuesText())
		}
		if !config.ValidEffort(value) {
			return "", fmt.Errorf("invalid config.model_reasoning_effort %q; valid values: %s", value, effortValuesText())
		}
		requested = value
	}
	if raw, present := arguments["reasoning-effort"]; present {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("reasoning-effort must be a string; valid values: %s", effortValuesText())
		}
		if value != "" {
			if !config.ValidEffort(value) {
				return "", fmt.Errorf("invalid reasoning-effort %q; valid values: %s", value, effortValuesText())
			}
			requested = value
		}
	}
	return requested, nil
}

// modelDescription describes the active provider's models for the model
// parameter of the start tool.
func modelDescription(cfg config.Provider) string {
	models := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		if model.Description == "" {
			models = append(models, model.ID)
			continue
		}
		models = append(models, fmt.Sprintf("%s (%s)", model.ID, model.Description))
	}
	return fmt.Sprintf("Model to use; default: %s. Available: %s", cfg.DefaultModel, strings.Join(models, ", "))
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
	mcp          *mcpserver.MCPServer
	mgr          *agent.Manager
	runner       *agent.Runner
	version      string
	providerName string
	toolName     string
	cfg          config.Provider

	callsMu sync.Mutex
	calls   map[string]context.CancelFunc
}

// Option configures a Server during construction.
type Option func(*Server)

// WithToolName sets the base name of the two MCP tools. The start tool is
// registered under name and the reply tool under name+"-reply".
func WithToolName(name string) Option {
	return func(s *Server) {
		s.toolName = name
	}
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidateToolName reports whether name is a valid SUBAGENT_MCP_TOOL_NAME value. A
// valid name is 1 to 64 characters from A-Z, a-z, 0-9, '_', and '-', and does
// not end with "-reply", which would collide with the generated reply tool.
func ValidateToolName(name string) error {
	if !toolNamePattern.MatchString(name) {
		return fmt.Errorf(
			"SUBAGENT_MCP_TOOL_NAME %q is invalid: the name must be 1 to 64 characters matching ^[A-Za-z0-9_-]{1,64}$",
			name,
		)
	}
	if strings.HasSuffix(name, "-reply") {
		return fmt.Errorf(
			"SUBAGENT_MCP_TOOL_NAME %q is invalid: the name must not end with \"-reply\" because the reply tool is registered as %s-reply",
			name,
			name,
		)
	}
	return nil
}

// New builds a Server for the active provider. cfg is the active provider's
// config entry and drives the model enum, the default model, and effort mapping.
func New(p provider.Provider, cfg config.Provider, version string, opts ...Option) *Server {
	s := &Server{
		mgr:          agent.NewManager(),
		version:      version,
		providerName: p.Name(),
		toolName:     defaultToolName,
		cfg:          cfg,
		calls:        make(map[string]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(s)
	}
	hooks := &mcpserver.Hooks{}
	hooks.AddBeforeCallTool(func(_ context.Context, id any, request *mcp.CallToolRequest) {
		key, ok := canonicalRequestID(id)
		if !ok {
			return
		}
		if request.Params.Meta == nil {
			request.Params.Meta = &mcp.Meta{}
		}
		if request.Params.Meta.AdditionalFields == nil {
			request.Params.Meta.AdditionalFields = make(map[string]any)
		}
		request.Params.Meta.AdditionalFields[requestIDMetaKey] = key
	})
	s.mcp = mcpserver.NewMCPServer(
		"subagent-mcp",
		version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithRecovery(),
		mcpserver.WithHooks(hooks),
	)
	s.mcp.AddNotificationHandler(cancelledNotificationMethod, s.handleCancelledNotification)
	s.runner = &agent.Runner{Provider: p, Emitter: s, Approver: s}
	s.mcp.AddTool(startTool(s.toolName, cfg), s.handleStart)
	s.mcp.AddTool(replyTool(s.toolName), s.handleReply)
	return s
}

func (s *Server) ServeStdio() error {
	return mcpserver.ServeStdio(s.mcp, mcpserver.WithWorkerPoolSize(32))
}

// beginCall derives a cancellable context for an in-flight tool call and
// registers its cancel function under the JSON-RPC request ID recorded by the
// before-call-tool hook. The returned stop function unregisters the call and
// releases the context; when the request has no ID, only cancellation applies.
func (s *Server) beginCall(ctx context.Context, req mcp.CallToolRequest) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if req.Params.Meta != nil && req.Params.Meta.ProgressToken != nil {
		ctx = context.WithValue(ctx, progressContextKey{}, &progressState{token: req.Params.Meta.ProgressToken})
	}
	key, ok := requestIDFromMeta(req)
	if !ok {
		return ctx, cancel
	}

	s.callsMu.Lock()
	s.calls[key] = cancel
	s.callsMu.Unlock()

	return ctx, func() {
		s.callsMu.Lock()
		delete(s.calls, key)
		s.callsMu.Unlock()
		cancel()
	}
}

// handleCancelledNotification cancels the in-flight call matching the
// requestId of a notifications/cancelled message. Unknown or malformed IDs are
// ignored.
func (s *Server) handleCancelledNotification(_ context.Context, notification mcp.JSONRPCNotification) {
	raw, ok := notification.Params.AdditionalFields["requestId"]
	if !ok {
		return
	}
	key, ok := canonicalRequestID(raw)
	if !ok {
		return
	}

	s.callsMu.Lock()
	cancel := s.calls[key]
	s.callsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type toolOutput struct {
	ThreadID string `json:"threadId" jsonschema_description:"ID of the subagent thread that produced this result."`
	Content  string `json:"content" jsonschema_description:"Human-readable report text from the subagent."`
}

func startTool(name string, cfg config.Provider) mcp.Tool {
	return mcp.NewTool(
		name,
		mcp.WithDescription("Start a new subagent coding-agent thread."),
		mcp.WithString(
			"prompt",
			mcp.Required(),
			mcp.Description("Task prompt to send to the new subagent thread."),
		),
		mcp.WithString(
			"model",
			mcp.Enum(cfg.ModelIDs()...),
			mcp.Description(modelDescription(cfg)),
		),
		mcp.WithString(
			"cwd",
			mcp.Description("Absolute path to an existing working directory; defaults to the subagent-mcp process working directory."),
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
			mcp.Enum(config.EffortValues...),
			mcp.Description("Reasoning effort: none, minimal, low, medium, high, xhigh, or max; defaults to high, or to config.model_reasoning_effort."),
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

func replyTool(baseName string) mcp.Tool {
	return mcp.NewTool(
		baseName+"-reply",
		mcp.WithDescription("Continue an existing subagent coding-agent thread."),
		mcp.WithString(
			"threadId",
			mcp.Required(),
			mcp.Description(fmt.Sprintf("Thread ID returned by a previous %s or %s-reply call.", baseName, baseName)),
		),
		mcp.WithString(
			"prompt",
			mcp.Required(),
			mcp.Description("Follow-up prompt to send to the existing subagent thread."),
		),
		mcp.WithOutputSchema[toolOutput](),
	)
}

func (s *Server) handleStart(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, endCall := s.beginCall(ctx, req)
	defer endCall()

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

	model := req.GetString("model", "")
	if model == "" {
		model = s.cfg.DefaultModel
	} else if !s.cfg.HasModel(model) {
		return mcp.NewToolResultError(fmt.Sprintf(
			"unknown model %q; available models: %s",
			model,
			strings.Join(s.cfg.ModelIDs(), ", "),
		)), nil
	}

	looseConfig, _ := arguments["config"].(map[string]any)
	requested, err := resolveEffort(arguments, looseConfig)
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
	if value, ok := looseConfig["max_turns"].(float64); ok && value > 0 && value <= maxConfiguredTurns {
		maxTurns = int(value)
	}

	writableRoots, err := parseWritableRoots(looseConfig)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	sess := s.mgr.Create(agent.Options{
		Model:           model,
		Cwd:             cwd,
		Sandbox:         sandbox,
		Approval:        approval,
		ReasoningEffort: requested,
		EffortSent:      s.cfg.MapEffort(requested),
		SystemPrompt:    systemPrompt,
		MaxTurns:        maxTurns,
		WritableRoots:   writableRoots,
	})
	created := time.Now()
	recorder := rollout.Open(sess.ID, created)
	sess.AttachRollout(recorder)
	meta := map[string]any{
		"session_id":        sess.ID,
		"id":                sess.ID,
		"timestamp":         created.UTC().Format(time.RFC3339Nano),
		"cwd":               cwd,
		"originator":        "subagent-mcp",
		"cli_version":       s.version,
		"source":            "mcp",
		"model_provider":    s.providerName,
		"base_instructions": map[string]any{"text": systemPrompt},
	}
	if resolvedCwd, err := filepath.EvalSymlinks(cwd); err == nil {
		if root, ok := repo.Root(resolvedCwd); ok {
			branch, commit := repo.Head(root)
			meta["git"] = map[string]any{"branch": branch, "commit_hash": commit}
		}
	}
	recorder.Write("session_meta", meta)

	text, err := s.runner.Run(ctx, sess, prompt)
	return resultWithThreadID(sess.ID, text, err), nil
}

func (s *Server) handleReply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, endCall := s.beginCall(ctx, req)
	defer endCall()

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
	if err := s.mcp.SendNotificationToClient(ctx, "subagent/event", map[string]any{
		"threadId": threadID,
		"msg":      msg,
	}); err != nil {
		log.Printf("subagent/event emit failed: %v", err)
	}

	state, ok := ctx.Value(progressContextKey{}).(*progressState)
	if !ok {
		return
	}
	event, _ := msg["type"].(string)
	summary, ok := progressSummary(event, msg)
	if !ok {
		return
	}
	if err := s.mcp.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
		"progressToken": state.token,
		"progress":      state.next(),
		"message":       summary,
	}); err != nil {
		log.Printf("notifications/progress emit failed: %v", err)
	}
}

// progressSummary returns the short notifications/progress message for a runner
// event, or false for events clients should not see progress for.
func progressSummary(event string, msg map[string]any) (string, bool) {
	switch event {
	case "task_started":
		return "started", true
	case "exec_command_begin":
		if command, _ := msg["command"].(string); command != "" {
			return truncateProgressSummary("shell: " + command), true
		}
		if paths, ok := msg["paths"].([]string); ok {
			return truncateProgressSummary("apply_patch: " + strings.Join(paths, ", ")), true
		}
		tool, _ := msg["tool"].(string)
		path, _ := msg["path"].(string)
		return truncateProgressSummary(tool + ": " + path), true
	case "agent_message":
		message, _ := msg["message"].(string)
		if firstLine, _, found := strings.Cut(message, "\n"); found {
			message = firstLine
		}
		return truncateProgressSummary("agent: " + message), true
	case "task_complete":
		return "completed", true
	case "error":
		message, _ := msg["message"].(string)
		return truncateProgressSummary("error: " + message), true
	default:
		return "", false
	}
}

// truncateProgressSummary caps a summary at progressSummaryLimit runes and
// appends an ellipsis when cut, without splitting a UTF-8 character.
func truncateProgressSummary(summary string) string {
	runes := []rune(summary)
	if len(runes) <= progressSummaryLimit {
		return summary
	}
	return string(runes[:progressSummaryLimit]) + "…"
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
				"subagent-mcp approval request (thread %s)\ntool: %s\ntarget: %s\nreason: %s",
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
