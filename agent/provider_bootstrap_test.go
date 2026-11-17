package agent

import (
	"os"
	"path/filepath"
	"testing"

	"norma/llm"
)

func TestConfigFromBootstrapReadsPrivateKeyFile(t *testing.T) {
	root := t.TempDir()
	old := runtimeSecretRoot
	runtimeSecretRoot = root
	t.Cleanup(func() { runtimeSecretRoot = old })
	key := filepath.Join(root, "cyberseek-api-key")
	if err := os.WriteFile(key, []byte("sk-abcdefghijklmnopqrstuvwxyz1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "artex-llm.json")
	document := `{"version":1,"providers":[{"provider":"openai","model":"adverserial/cyberseek","transport":"attested-gateway","base_url":"http://attested-inference-gateway:8787/v1","api_key_file":"` + key + `"}]}`
	if err := os.WriteFile(config, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := configsFromBootstrap(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("bootstrap config count = %d", len(configs))
	}
	cfg := configs[0]
	if cfg.Format != llm.FormatOpenAI || cfg.Model != "adverserial/cyberseek" || cfg.APIKey == "" || cfg.Proxy != "" || cfg.BaseURL != attestedGatewayBaseURL {
		t.Fatalf("unexpected bootstrap config: %#v", cfg)
	}
}

func TestConfigsFromBootstrapKeepsBothProviderKeysOutOfDatabase(t *testing.T) {
	root := t.TempDir()
	old := runtimeSecretRoot
	runtimeSecretRoot = root
	t.Cleanup(func() { runtimeSecretRoot = old })
	anthropic := filepath.Join(root, "anthropic-api-key")
	openai := filepath.Join(root, "openai-api-key")
	for path, value := range map[string]string{
		anthropic: "ant-abcdefghijklmnopqrstuvwxyz1234567890",
		openai:    "sk-abcdefghijklmnopqrstuvwxyz1234567890",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "artex-llm.json")
	document := `{"version":1,"providers":[{"provider":"anthropic","api_key_file":"` + anthropic + `"},{"provider":"openai","api_key_file":"` + openai + `"}]}`
	if err := os.WriteFile(config, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := configsFromBootstrap(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0].Format != llm.FormatAnthropic || configs[1].Format != llm.FormatOpenAI {
		t.Fatalf("unexpected bootstrap chain: %#v", configs)
	}
	for _, cfg := range configs {
		if cfg.APIKey == "" || cfg.Proxy != runtimeBootstrapProxy {
			t.Fatalf("unsafe bootstrap config: %#v", cfg)
		}
	}
}

func TestConfigFromBootstrapRejectsEscapesBroadModesAndHTTP(t *testing.T) {
	root := t.TempDir()
	old := runtimeSecretRoot
	runtimeSecretRoot = root
	t.Cleanup(func() { runtimeSecretRoot = old })
	outside := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(outside, []byte("sk-abcdefghijklmnopqrstuvwxyz1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"escape":           `{"version":1,"providers":[{"provider":"openai","api_key_file":"` + outside + `"}]}`,
		"http":             `{"version":1,"providers":[{"provider":"openai","base_url":"http://example.test/v1","api_key_file":"` + outside + `"}]}`,
		"query":            `{"version":1,"providers":[{"provider":"openai","base_url":"https://example.test/v1?token=leak","api_key_file":"` + outside + `"}]}`,
		"fragment":         `{"version":1,"providers":[{"provider":"openai","base_url":"https://example.test/v1#fragment","api_key_file":"` + outside + `"}]}`,
		"wrong-host":       `{"version":1,"providers":[{"provider":"openai","base_url":"https://example.test/v1","api_key_file":"` + outside + `"}]}`,
		"direct-cyberseek": `{"version":1,"providers":[{"provider":"openai","model":"adverserial/cyberseek","base_url":"https://api.adverserial.ai/v1","api_key_file":"` + outside + `"}]}`,
		"wrong-gateway":    `{"version":1,"providers":[{"provider":"openai","model":"adverserial/cyberseek","transport":"attested-gateway","base_url":"http://evil-gateway:8787/v1","api_key_file":"` + outside + `"}]}`,
		"unknown":          `{"version":1,"providers":[{"provider":"openai","api_key_file":"` + outside + `","secret":"value"}]}`,
		"duplicate":        `{"version":1,"providers":[{"provider":"openai","api_key_file":"` + outside + `"},{"provider":"openai","api_key_file":"` + outside + `"}]}`,
	} {
		config := filepath.Join(root, name+".json")
		if err := os.WriteFile(config, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := configsFromBootstrap(config); err == nil {
			t.Fatalf("%s bootstrap should be rejected", name)
		}
	}
	tooBroad := filepath.Join(root, "broad-key")
	if err := os.WriteFile(tooBroad, []byte("sk-abcdefghijklmnopqrstuvwxyz1234567890"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeSecret(tooBroad, 4096); err == nil {
		t.Fatal("group/world-readable key must be rejected")
	}
}

func TestInvalidBootstrapDoesNotFallBackToEnvironmentKey(t *testing.T) {
	t.Setenv("ARTEX_LLM_BOOTSTRAP_FILE", "/run/adverserial/current/missing")
	t.Setenv("OPENAI_API_KEY", "sk-abcdefghijklmnopqrstuvwxyz1234567890")
	t.Setenv("ARTEX_LLM_PROVIDER", "openai")
	if _, ok := FromEnv(); ok {
		t.Fatal("invalid measured-runtime bootstrap must fail closed")
	}
}
