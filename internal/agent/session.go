package agent

import (
	"sync"

	"github.com/Geek0x0/ds-mcp/internal/policy"

	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"
)

const DefaultMaxTurns = 50

const DefaultSystemPrompt = `You are ds-mcp, a coding agent powered by DeepSeek. You work inside a fixed working directory using three tools:

- shell: run a bash command in the working directory
- read_file: read a file (relative paths resolve against the working directory)
- write_file: create or overwrite a whole file (parent directories are created)

Work autonomously on the task you are given: inspect what you need, make the smallest change that satisfies the request, and verify it when possible. Some calls may be denied by the sandbox policy or the user; when that happens, adapt your approach or explain the blocker instead of repeating the same call. When the task is done, reply WITHOUT any tool call: summarize what you did, list changed files, and how you verified the result.`

type Options struct {
	Model           string
	ReasoningEffort string
	Cwd             string
	Sandbox         policy.Sandbox
	Approval        policy.ApprovalPolicy
	SystemPrompt    string
	MaxTurns        int
}

type Session struct {
	ID string

	model           string
	reasoningEffort string
	cwd             string
	sandbox         policy.Sandbox
	approval        policy.ApprovalPolicy
	maxTurns        int
	messages        []openai.ChatCompletionMessage
	mu              sync.Mutex
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

func (m *Manager) Create(o Options) *Session {
	if o.Model == "" {
		o.Model = "deepseek-v4-pro"
	}
	if o.ReasoningEffort == "" {
		o.ReasoningEffort = "high"
	}
	if o.MaxTurns <= 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	if o.SystemPrompt == "" {
		o.SystemPrompt = DefaultSystemPrompt
	}

	session := &Session{
		ID:              uuid.NewString(),
		model:           o.Model,
		reasoningEffort: o.ReasoningEffort,
		cwd:             o.Cwd,
		sandbox:         o.Sandbox,
		approval:        o.Approval,
		maxTurns:        o.MaxTurns,
		messages: []openai.ChatCompletionMessage{{
			Role:    openai.ChatMessageRoleSystem,
			Content: o.SystemPrompt,
		}},
	}

	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()

	return session
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	return session, ok
}
