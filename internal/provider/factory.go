package provider

import (
	"fmt"
	"sync"

	"github.com/Geek0x0/subagent-mcp/internal/config"
)

// Constructor builds an adapter for one configured provider.
type Constructor func(name string, cfg config.Provider, apiKey string) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Constructor{}
)

// Register makes an adapter available for a config api value; adapters call it from init.
func Register(api string, ctor Constructor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[api] = ctor
}

// New builds the adapter for cfg.API.
func New(name string, cfg config.Provider, apiKey string) (Provider, error) {
	registryMu.RLock()
	ctor, ok := registry[cfg.API]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported api %q (is its adapter package imported?)", cfg.API)
	}
	return ctor(name, cfg, apiKey)
}
