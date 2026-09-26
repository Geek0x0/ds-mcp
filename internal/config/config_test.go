package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validTOML = `

[providers.deepseek]
api = "chat-completions"
base_url = "https://api.deepseek.com"
env_key = "DS_TEST_KEY"
default_model = "deepseek-flash"
models = [
  { id = "deepseek-flash", description = "fast" },
  { id = "deepseek-v4-pro" },
]
effort_map = { medium = "high", xhigh = "max" }

[providers.anthropic]
api = "messages"
env_key = "ANT_TEST_KEY"
default_model = "claude-sonnet-5"
models = [{ id = "claude-sonnet-5" }]
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validTOML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	p, err := cfg.Provider("deepseek")
	if err != nil {
		t.Fatalf("Provider(deepseek) error = %v", err)
	}
	if p.API != APIChatCompletions || p.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("Provider(deepseek) = %#v", p)
	}
	if !reflect.DeepEqual(p.ModelIDs(), []string{"deepseek-flash", "deepseek-v4-pro"}) {
		t.Fatalf("ModelIDs() = %v", p.ModelIDs())
	}
	if p.MapEffort("xhigh") != "max" || p.MapEffort("low") != "low" {
		t.Fatalf("MapEffort wrong")
	}
	anthropic := cfg.Providers["anthropic"]
	if anthropic.BaseURL != "https://api.anthropic.com" || anthropic.MaxOutputTokens != 64000 {
		t.Fatalf("defaults not applied: %#v", anthropic)
	}
	if !reflect.DeepEqual(cfg.EnvKeys(), []string{"ANT_TEST_KEY", "DS_TEST_KEY"}) {
		t.Fatalf("EnvKeys() = %v", cfg.EnvKeys())
	}
	if !reflect.DeepEqual(cfg.ProviderNames(), []string{"anthropic", "deepseek"}) {
		t.Fatalf("ProviderNames() = %v", cfg.ProviderNames())
	}
	if cfg.Path == "" {
		t.Fatalf("Path not recorded")
	}
}

func TestLoadRelativePathStoredAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(validTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// t.Chdir sets PWD to the path it was given and filepath.Abs prefers $PWD,
	// so cfg.Path may keep a symlinked form of the cwd (for example /var rather
	// than /private/var on macOS). Resolve both sides before comparing; the
	// IsAbs check is what guards the fix itself.
	check := func(t *testing.T, base string) {
		t.Helper()
		cfg, err := Load("config.toml")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !filepath.IsAbs(cfg.Path) {
			t.Fatalf("cfg.Path = %q, want an absolute path", cfg.Path)
		}
		got, err := filepath.EvalSymlinks(cfg.Path)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", cfg.Path, err)
		}
		want, err := filepath.EvalSymlinks(filepath.Join(base, "config.toml"))
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", filepath.Join(base, "config.toml"), err)
		}
		if got != want {
			t.Fatalf("cfg.Path resolves to %q, want %q", got, want)
		}
	}

	check(t, dir)

	t.Run("via symlinked cwd", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		t.Chdir(link)
		check(t, link)
	})
}

func TestAPIKeyFor(t *testing.T) {
	cfg, err := Load(writeConfig(t, validTOML))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DS_TEST_KEY", "")
	if _, err := cfg.APIKeyFor("deepseek"); err == nil || !strings.Contains(err.Error(), "DS_TEST_KEY") {
		t.Fatalf("APIKeyFor() error = %v, want mention of DS_TEST_KEY", err)
	}
	t.Setenv("DS_TEST_KEY", "sk-1")
	if key, err := cfg.APIKeyFor("deepseek"); err != nil || key != "sk-1" {
		t.Fatalf("APIKeyFor() = %q, %v", key, err)
	}
	if _, err := cfg.APIKeyFor("nope"); err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("APIKeyFor(nope) error = %v, want it to name the unknown provider and list the configured ones", err)
	}
}

func TestProviderLookup(t *testing.T) {
	cfg, err := Load(writeConfig(t, validTOML))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Provider("deepseek"); err != nil {
		t.Fatalf("Provider(deepseek) error = %v", err)
	}
	_, err = cfg.Provider("nope")
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("Provider(nope) error = %v, want it to name the unknown provider and list anthropic and deepseek", err)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown top key", "typo = 1\n", "typo"},
		{"unknown provider key", strings.Replace(validTOML, `env_key = "ANT_TEST_KEY"`, "env_key = \"ANT_TEST_KEY\"\nmodel = \"x\"", 1), "model"},
		{"bad api", strings.Replace(validTOML, `api = "messages"`, `api = "grpc"`, 1), "providers.anthropic.api"},
		{"missing env_key", strings.Replace(validTOML, `env_key = "ANT_TEST_KEY"`, "", 1), "providers.anthropic.env_key"},
		{"default not in models", strings.Replace(validTOML, `default_model = "claude-sonnet-5"`, `default_model = "claude-opus-5"`, 1), "providers.anthropic.default_model"},
		{"empty models", strings.Replace(validTOML, `models = [{ id = "claude-sonnet-5" }]`, "models = []", 1), "providers.anthropic.models"},
		{"duplicate model", strings.Replace(validTOML, `{ id = "deepseek-v4-pro" }`, `{ id = "deepseek-flash" }`, 1), "providers.deepseek.models"},
		{"bad effort key", strings.Replace(validTOML, `medium = "high"`, `turbo = "high"`, 1), "providers.deepseek.effort_map"},
		{"bad effort value", strings.Replace(validTOML, `medium = "high"`, `medium = "turbo"`, 1), "providers.deepseek.effort_map"},
		{"negative max tokens", strings.Replace(validTOML, `api = "messages"`, "api = \"messages\"\nmax_output_tokens = -1", 1), "providers.anthropic.max_output_tokens"},
		{"no providers", "providers = {}\n", "at least one provider"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "config.example.toml") {
		t.Fatalf("Load() error = %v, want path and example hint", err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("SUBAGENT_MCP_CONFIG", "/custom/config.toml")
	if got, _ := DefaultPath(); got != "/custom/config.toml" {
		t.Fatalf("DefaultPath() = %q", got)
	}
	t.Setenv("SUBAGENT_MCP_CONFIG", "")
	t.Setenv("HOME", "/home/test")
	if got, _ := DefaultPath(); got != "/home/test/.config/subagent-mcp/config.toml" {
		t.Fatalf("DefaultPath() = %q", got)
	}
}

func TestValidEffort(t *testing.T) {
	for _, value := range EffortValues {
		if !ValidEffort(value) {
			t.Errorf("ValidEffort(%q) = false", value)
		}
	}
	if ValidEffort("turbo") {
		t.Errorf("ValidEffort(turbo) = true")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	// config.example.toml still carries the legacy active_provider key until a
	// later work unit removes it; strip that key here so the rest of the
	// example loads under the name-keyed config (strict decoding rejects the
	// removed field).
	data, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatalf("read config.example.toml: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "active_provider") {
			continue
		}
		lines = append(lines, line)
	}
	cfg, err := Load(writeConfig(t, strings.Join(lines, "\n")))
	if err != nil {
		t.Fatalf("config.example.toml does not load: %v", err)
	}
	for _, name := range []string{"deepseek", "openai", "anthropic"} {
		if _, ok := cfg.Providers[name]; !ok {
			t.Errorf("example lacks provider %q", name)
		}
	}
}
