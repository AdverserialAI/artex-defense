package server

import (
	"encoding/json"
	"testing"

	"artex/agent"
	"artex/db"
)

func TestEnglishRuntimeDefaults(t *testing.T) {
	for key, prompt := range agent.BuiltinPromptSeeds() {
		t.Run(key, func(t *testing.T) { requireNoHan(t, prompt) })
	}
	for key, prompt := range agent.PlannerDomainSeeds() {
		t.Run("planner/"+key, func(t *testing.T) { requireNoHan(t, prompt) })
	}
	for _, seed := range agent.WorkerDomainSeeds() {
		t.Run("worker/"+seed.NKey, func(t *testing.T) { requireNoHan(t, seed.Body) })
	}
	for _, seed := range agent.BuiltinToolSeeds() {
		t.Run("tool/"+seed.Key, func(t *testing.T) {
			requireNoHan(t, seed.Desc)
			schema, err := json.Marshal(seed.Schema)
			if err != nil {
				t.Fatal(err)
			}
			requireNoHan(t, string(schema))
		})
	}
	requireNoHan(t, agent.TraceCleanerDefaultPrompt)
	requireNoHan(t, agent.RetesterDefaultPrompt)
}

func TestEnglishSchemaPreservesStructureAndCustomText(t *testing.T) {
	prior := "\u610f\u56fe id\uff08= work \u53e5\u67c4\uff09"
	schema := map[string]any{"type": "object", "properties": map[string]any{"intent_id": map[string]any{"type": "integer", "description": prior}, "custom": map[string]any{"type": "string", "description": "My custom description", "default": "keep-me"}}, "required": []string{"intent_id"}}
	encoded, _ := json.Marshal(schema)
	tool := &db.Tool{System: true, Schema: encoded}
	localizeToolSchema(tool)
	var got map[string]any
	if err := json.Unmarshal(tool.Schema, &got); err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	if props["intent_id"].(map[string]any)["description"] != "Intent ID (work handle)" {
		t.Fatalf("annotation not translated: %s", tool.Schema)
	}
	custom := props["custom"].(map[string]any)
	if custom["description"] != "My custom description" || custom["default"] != "keep-me" {
		t.Fatal("custom schema data changed")
	}
	if props["intent_id"].(map[string]any)["type"] != "integer" || got["required"].([]any)[0] != "intent_id" {
		t.Fatal("schema structure changed")
	}
}

func TestEnglishDefaultsUpgradePreservesCustomization(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer m.Close()
	seedPrompts(m.pg)
	s := &Server{m: m}
	var old struct {
		Prompts          map[string]string `json:"prompts"`
		PlannerTemplates map[string]string `json:"planner_templates"`
	}
	if err = json.Unmarshal(legacyDefaultsJSON, &old); err != nil {
		t.Fatal(err)
	}
	planner, err := m.pg.GetAgentByKey("planner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := m.pg.GetAgentByKey("worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.pg.ResetPromptToDefault(planner.ID, old.Prompts["planner"]); err != nil {
		t.Fatal(err)
	}
	const custom = "My administrator-defined Worker prompt"
	if _, err = m.pg.ResetPromptToDefault(worker.ID, custom); err != nil {
		t.Fatal(err)
	}
	// Scenario seeds historically use builtin=false; exact old defaults still upgrade.
	for key, prior := range old.PlannerTemplates {
		if _, err = m.pg.Exec(`UPDATE planner_templates SET body=$1,builtin=false WHERE nkey=$2`, prior, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.pg.Exec(`DELETE FROM settings WHERE key='english_defaults_v1'`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateEnglishDefaults(); err != nil {
		t.Fatal(err)
	}
	got, err := m.pg.CurrentPrompt(planner.ID)
	if err != nil || got != agent.BuiltinPromptSeeds()["planner"] {
		t.Fatalf("planner default not upgraded: %v", err)
	}
	got, err = m.pg.CurrentPrompt(worker.ID)
	if err != nil || got != custom {
		t.Fatalf("custom prompt changed: %q %v", got, err)
	}
	if err = s.migrateEnglishDefaults(); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	internal, err := m.pg.GetPlannerTemplateByNKey("internal-penetration-testing")
	if err != nil || internal == nil {
		t.Fatalf("English scenario alias: %v", err)
	}
	if internal.Body != agent.PlannerDomainSeeds()[internal.NKey] {
		t.Fatal("legacy scenario default was not translated")
	}
}
