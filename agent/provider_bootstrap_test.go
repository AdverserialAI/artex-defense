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

func bootstrapRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := runtimeSecretRoot
	runtimeSecretRoot = root
	t.Cleanup(func() { runtimeSecretRoot = old })
	return root
}

func writeSecret(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapWithNoProviderYetIsValidAndIdle(t *testing.T) {
	root := bootstrapRoot(t)
	config := filepath.Join(root, "bootstrap.json")
	writeSecret(t, config, `{"version":1,"providers":[]}`)
	configs, err := configsFromBootstrap(config)
	if err != nil || len(configs) != 0 {
		t.Fatalf("an empty provider list must be accepted as waiting: %v %d", err, len(configs))
	}
	t.Setenv("ARTEX_LLM_BOOTSTRAP_FILE", config)
	t.Setenv("ANTHROPIC_API_KEY", "ant-must-never-be-used-as-a-fallback-0000")
	if fromEnv, ok := ConfigsFromEnv(); !ok || len(fromEnv) != 0 {
		t.Fatalf("waiting bootstrap must not fall back to an environment key: %v %d", ok, len(fromEnv))
	}
}

func TestBootstrapCustomEndpointMustBePublicHTTPS(t *testing.T) {
	root := bootstrapRoot(t)
	key := filepath.Join(root, "key")
	writeSecret(t, key, "sk-abcdefghijklmnopqrstuvwxyz1234567890")
	config := filepath.Join(root, "bootstrap.json")
	load := func(provider string) ([]Config, error) {
		writeSecret(t, config, `{"version":1,"providers":[`+provider+`]}`)
		return configsFromBootstrap(config)
	}
	t.Setenv("ARTEX_LLM_BOOTSTRAP_EGRESS", "direct")
	configs, err := load(`{"provider":"openai","model":"my-model","endpoint":"custom","base_url":"https://llm.example.com/v1","api_key_file":"` + key + `"}`)
	if err != nil || len(configs) != 1 || configs[0].BaseURL != "https://llm.example.com/v1" || configs[0].Proxy != "" {
		t.Fatalf("custom public HTTPS endpoint rejected: %v %#v", err, configs)
	}
	for _, bad := range []string{
		`"endpoint":"custom","base_url":"http://llm.example.com/v1"`, // not HTTPS
		`"endpoint":"custom","base_url":"https://localhost/v1"`,      // this machine
		`"endpoint":"custom","base_url":"https://127.0.0.1/v1"`,
		`"endpoint":"custom","base_url":"https://169.254.169.254/v1"`, // the cloud metadata service
		`"endpoint":"custom","base_url":"https://metadata.google.internal/v1"`,
		`"endpoint":"custom","base_url":"https://10.0.0.5/v1"`, // a private neighbour
		`"endpoint":"custom","base_url":"https://[::1]/v1"`,
		`"endpoint":"custom","base_url":"https://gateway/v1"`,                 // a bare container name
		`"endpoint":"custom","base_url":"https://user:pw@llm.example.com/v1"`, // credentials in the address
		`"endpoint":"custom","base_url":"https://llm.example.com/v1?key=x"`,
		`"endpoint":"custom"`, // custom without an address
		`"endpoint":"anything-else","base_url":"https://llm.example.com/v1"`,
		`"base_url":"https://llm.example.com/v1"`, // not marked custom: only the provider's own host
		`"endpoint":"custom","transport":"attested-gateway","model":"adverserial/cyberseek","base_url":"http://attested-inference-gateway:8787/v1"`,
	} {
		if _, err := load(`{"provider":"openai","model":"my-model",` + bad + `,"api_key_file":"` + key + `"}`); err == nil {
			t.Fatalf("accepted an endpoint it must refuse: %s", bad)
		}
	}
}

func TestBootstrapEgressIsTheProxyUnlessTheDeploymentSaysDirect(t *testing.T) {
	root := bootstrapRoot(t)
	key := filepath.Join(root, "key")
	writeSecret(t, key, "ant-abcdefghijklmnopqrstuvwxyz1234567890")
	config := filepath.Join(root, "bootstrap.json")
	writeSecret(t, config, `{"version":1,"providers":[{"provider":"anthropic","api_key_file":"`+key+`"}]}`)
	configs, err := configsFromBootstrap(config)
	if err != nil || configs[0].Proxy != runtimeBootstrapProxy {
		t.Fatalf("default egress must be the sibling proxy: %v %#v", err, configs)
	}
	t.Setenv("ARTEX_LLM_BOOTSTRAP_EGRESS", "direct")
	configs, err = configsFromBootstrap(config)
	if err != nil || configs[0].Proxy != "" {
		t.Fatalf("direct egress must use no proxy: %v %#v", err, configs)
	}
}

func TestBootstrapFingerprintFollowsTheDocumentAndItsKeysWithoutRevealingThem(t *testing.T) {
	if BootstrapFingerprint() != "" {
		t.Fatal("no bootstrap configured must give an empty fingerprint")
	}
	root := bootstrapRoot(t)
	key := filepath.Join(root, "key")
	config := filepath.Join(root, "bootstrap.json")
	t.Setenv("ARTEX_LLM_BOOTSTRAP_FILE", config)
	if BootstrapFingerprint() != "unreadable" {
		t.Fatal("a missing document must be reported as unreadable")
	}
	writeSecret(t, config, `{"version":1,"providers":[]}`)
	waiting := BootstrapFingerprint()
	writeSecret(t, key, "sk-abcdefghijklmnopqrstuvwxyz1234567890")
	writeSecret(t, config, `{"version":1,"providers":[{"provider":"openai","api_key_file":"`+key+`"}]}`)
	connected := BootstrapFingerprint()
	writeSecret(t, key, "sk-zyxwvutsrqponmlkjihgfedcba0987654321")
	replaced := BootstrapFingerprint()
	if waiting == connected || connected == replaced || waiting == replaced || len(connected) != 64 {
		t.Fatalf("fingerprint did not follow the changes: %q %q %q", waiting, connected, replaced)
	}
	if connected == BootstrapFingerprint() || replaced != BootstrapFingerprint() {
		t.Fatal("fingerprint must be stable while nothing changes")
	}
	for _, secret := range []string{"sk-abcdefghijklmnopqrstuvwxyz1234567890", "sk-zyxwvutsrqponmlkjihgfedcba0987654321"} {
		if replaced == secret || connected == secret {
			t.Fatal("fingerprint must not be the key")
		}
	}
}
