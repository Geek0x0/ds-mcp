package provider_test

import (
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/provider"
	"github.com/Geek0x0/subagent-mcp/internal/provider/chatcompletions"
	"github.com/Geek0x0/subagent-mcp/internal/provider/messages"
	"github.com/Geek0x0/subagent-mcp/internal/provider/responses"
)

// Compile-time assertions: each real adapter must expose the optional
// ModelLister capability a config checker uses to validate the provider's
// credentials and model ids.
var (
	_ provider.ModelLister = (*chatcompletions.Adapter)(nil)
	_ provider.ModelLister = (*responses.Adapter)(nil)
	_ provider.ModelLister = (*messages.Adapter)(nil)
)

func TestAdaptersImplementModelLister(t *testing.T) {
	for _, api := range []string{config.APIChatCompletions, config.APIResponses, config.APIMessages} {
		p, err := provider.New("test", config.Provider{API: api, BaseURL: "http://127.0.0.1:1"}, "key")
		if err != nil {
			t.Fatalf("New(%q) error: %v", api, err)
		}
		if _, ok := p.(provider.ModelLister); !ok {
			t.Errorf("%s adapter does not implement provider.ModelLister", api)
		}
	}
}
