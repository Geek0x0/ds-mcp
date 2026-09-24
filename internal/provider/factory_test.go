package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/Geek0x0/subagent-mcp/internal/config"
)

type nopProvider struct{ name string }

func (p nopProvider) Name() string { return p.name }
func (p nopProvider) Turn(context.Context, TurnRequest, func(string)) (*TurnResult, error) {
	return &TurnResult{}, nil
}

func TestFactory(t *testing.T) {
	Register("test-api", func(name string, _ config.Provider, _ string) (Provider, error) {
		return nopProvider{name: name}, nil
	})
	p, err := New("mine", config.Provider{API: "test-api"}, "key")
	if err != nil || p.Name() != "mine" {
		t.Fatalf("New() = %v, %v", p, err)
	}
	if _, err := New("x", config.Provider{API: "grpc"}, "key"); err == nil || !strings.Contains(err.Error(), "grpc") {
		t.Fatalf("New() unknown api error = %v", err)
	}
}
