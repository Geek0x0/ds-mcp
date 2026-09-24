package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Geek0x0/subagent-mcp/internal/config"
	"github.com/Geek0x0/subagent-mcp/internal/provider/chatcompletions"
	"github.com/Geek0x0/subagent-mcp/internal/sandbox"
	dsserver "github.com/Geek0x0/subagent-mcp/internal/server"
)

const version = "0.5.0"

func main() {
	sandbox.MaybeRunHelper()
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("subagent-mcp %s\n", version)
		return
	}

	var options []dsserver.Option
	if toolName := os.Getenv("SUBAGENT_MCP_TOOL_NAME"); toolName != "" {
		if err := dsserver.ValidateToolName(toolName); err != nil {
			log.Fatal(err)
		}
		options = append(options, dsserver.WithToolName(toolName))
	}

	key, err := resolveAPIKey()
	if err != nil {
		log.Fatal(err)
	}
	baseURL := os.Getenv("DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	p, err := chatcompletions.New("deepseek", config.Provider{API: config.APIChatCompletions, BaseURL: baseURL}, key)
	if err != nil {
		log.Fatal(err)
	}
	if err := dsserver.New(p, version, options...).ServeStdio(); err != nil {
		log.Fatal(err)
	}
}

func resolveAPIKey() (string, error) {
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return key, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for ~/.config/ds-mcp/auth.json: %w", err)
	}
	path := filepath.Join(home, ".config", "ds-mcp", "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errAPIKeyRequired
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("%s has overly permissive permissions (mode %o); run 'chmod 600 %s' and try again", path, mode, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errAPIKeyRequired
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var auth struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("%s contains invalid JSON: %w", path, err)
	}
	if auth.APIKey == "" {
		return "", fmt.Errorf("%s has an empty or missing api_key field", path)
	}
	return auth.APIKey, nil
}

var errAPIKeyRequired = errors.New("DEEPSEEK_API_KEY environment variable or ~/.config/ds-mcp/auth.json is required")
