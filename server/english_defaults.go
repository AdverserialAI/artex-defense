package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"

	"artex/agent"
	"artex/db"
	"artex/intercept"
)

// Historical defaults are recognition data, not text emitted to new users.
// Exact comparisons preserve administrator edits and existing stable identifiers.
//
//go:embed english_legacy_defaults.json
var legacyDefaultsJSON []byte

//go:embed english_legacy_text.json
var legacyTextJSON []byte

var legacyEnglishText = func() map[string]string {
	var values map[string]string
	if err := json.Unmarshal(legacyTextJSON, &values); err != nil {
		panic(err)
	}
	return values
}()

func englishSchemaAnnotations(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, item := range node {
			if key == "description" || key == "title" {
				if text, ok := item.(string); ok {
					if translated, known := legacyEnglishText[text]; known {
						node[key] = translated
					}
				}
			} else {
				englishSchemaAnnotations(item)
			}
		}
	case []any:
		for _, item := range node {
			englishSchemaAnnotations(item)
		}
	}
}

func localizeToolSchema(tool *db.Tool) {
	if tool == nil || !tool.System || len(tool.Schema) == 0 {
		return
	}
	var schema any
	if json.Unmarshal(tool.Schema, &schema) != nil {
		return
	}
	englishSchemaAnnotations(schema)
	if encoded, err := json.Marshal(schema); err == nil {
		tool.Schema = encoded
	}
}

// migrateEnglishDefaults updates only byte-identical, code-owned old defaults.
// Existing custom prompts, reports, findings, and conversation history are kept.
func (s *Server) migrateEnglishDefaults() error {
	pg := s.m.pg
	const flag = "english_defaults_v1"
	if value, _, err := pg.GetSetting(flag); err != nil {
		return err
	} else if value == "true" {
		return nil
	}
	var old struct {
		Prompts          map[string]string        `json:"prompts"`
		PlannerTemplates map[string]string        `json:"planner_templates"`
		WorkerTemplates  []agent.WorkerDomainSeed `json:"worker_templates"`
		TraceCleaner     string                   `json:"trace_cleaner"`
		Retester         string                   `json:"retester"`
		Judge            string                   `json:"judge"`
	}
	if err := json.Unmarshal(legacyDefaultsJSON, &old); err != nil {
		return err
	}
	next := agent.BuiltinPromptSeeds()
	old.Prompts["trace_cleaner"] = old.TraceCleaner
	old.Prompts["retester"] = old.Retester
	next["trace_cleaner"] = agent.TraceCleanerDefaultPrompt
	next["retester"] = agent.RetesterDefaultPrompt
	for key, prior := range old.Prompts {
		replacement, ok := next[key]
		if !ok || prior == replacement {
			continue
		}
		a, err := pg.GetAgentByKey(key)
		if err != nil {
			return err
		}
		if a == nil {
			continue
		}
		current, err := pg.CurrentPrompt(a.ID)
		if err != nil {
			return err
		}
		if current == prior {
			if _, err = pg.ResetPromptToDefault(a.ID, replacement); err != nil {
				return fmt.Errorf("translate %s default prompt: %w", key, err)
			}
		}
	}
	planners := agent.PlannerDomainSeeds()
	old.PlannerTemplates[db.ExternalPentestTemplateName] = old.Prompts["planner"]
	planners[db.ExternalPentestTemplateName] = next["planner"]
	workers := map[string]string{db.ExternalPentestTemplateName: next["worker"]}
	oldWorkers := map[string]string{db.ExternalPentestTemplateName: old.Prompts["worker"]}
	for _, seed := range agent.WorkerDomainSeeds() {
		workers[seed.NKey] = seed.Body
	}
	for _, seed := range old.WorkerTemplates {
		oldWorkers[seed.NKey] = seed.Body
	}
	tx, err := pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, group := range []struct {
		table, column string
		before, after map[string]string
	}{
		{"planner_templates", "planner_prompt_override", old.PlannerTemplates, planners},
		{"worker_templates", "worker_prompt_override", oldWorkers, workers},
	} {
		for key, prior := range group.before {
			replacement, ok := group.after[key]
			if !ok || prior == replacement {
				continue
			}
			if _, err = tx.Exec(`UPDATE `+group.table+` SET body=$1,updated_at=now() WHERE nkey=$2 AND body=$3`, replacement, key, prior); err != nil {
				return err
			}
			if _, err = tx.Exec(`UPDATE tasks SET `+group.column+`=$1 WHERE `+group.column+`=$2`, replacement, prior); err != nil {
				return err
			}
		}
	}
	for key, name := range scenarioDisplayNames {
		if _, err = tx.Exec(`UPDATE task_categories SET name=$1,updated_at=now() WHERE nkey=$2 AND name=$2`, name, key); err != nil {
			return err
		}
		for _, table := range []string{"planner_templates", "worker_templates"} {
			if _, err = tx.Exec(`UPDATE `+table+` SET name=$1,updated_at=now() WHERE nkey=$2 AND name=$2`, name, key); err != nil {
				return err
			}
		}
	}
	for _, metadata := range []struct{ key, before, after string }{
		{"trace_cleaner", "\u75d5\u8ff9\u6e05\u7406", "Trace Cleanup"},
		{"retester", "\u6f0f\u6d1e\u590d\u6d4b", "Finding Retest"},
		{"reporter", "\u62a5\u544a\u64b0\u5199", "Report Writer"},
		{"vuln_dispatcher", "\u6f0f\u6d1e\u6d3e\u53d1", "Finding Dispatcher"},
	} {
		if _, err = tx.Exec(`UPDATE agents SET name=$1 WHERE key=$2 AND name=$3`, metadata.after, metadata.key, metadata.before); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT key,description FROM agents WHERE key IN ('trace_cleaner','retester','reporter','vuln_dispatcher')`)
	if err != nil {
		return err
	}
	descriptions := map[string]string{}
	for rows.Next() {
		var key, description string
		if err = rows.Scan(&key, &description); err != nil {
			rows.Close()
			return err
		}
		if replacement, known := legacyEnglishText[description]; known {
			descriptions[key] = replacement
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for key, replacement := range descriptions {
		if _, err = tx.Exec(`UPDATE agents SET description=$1 WHERE key=$2`, replacement, key); err != nil {
			return err
		}
	}
	for prior, replacement := range interceptRuleDisplayNames {
		if _, err = tx.Exec(`UPDATE intercept_rules SET name=$1 WHERE name=$2`, replacement, prior); err != nil {
			return err
		}
	}
	for prior, replacement := range interceptRuleDisplayMessages {
		if _, err = tx.Exec(`UPDATE intercept_rules SET message=$1 WHERE message=$2`, replacement, prior); err != nil {
			return err
		}
	}
	for prior, replacement := range assetInterceptDisplayNotes {
		if _, err = tx.Exec(`UPDATE asset_intercept_rules SET note=$1 WHERE builtin AND note=$2`, replacement, prior); err != nil {
			return err
		}
	}
	for key, prior := range legacyToolDescriptions {
		replacement, ok := toolDisplayDescriptions[key]
		if !ok {
			continue
		}
		if _, err = tx.Exec(`UPDATE tools SET description=$1,updated_at=now() WHERE system AND key=$2 AND description=$3`, replacement, key, prior); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE settings SET value=$1 WHERE key='llm_judge_prompt' AND value=$2`, intercept.DefaultJudgePrompt, old.Judge); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Schema annotation changes preserve all structural keys and configured defaults.
	tools, err := pg.ListTools()
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if !tool.System {
			continue
		}
		before := string(tool.Schema)
		localizeToolSchema(tool)
		if string(tool.Schema) != before {
			if _, err = pg.Exec(`UPDATE tools SET schema=$1,updated_at=now() WHERE system AND key=$2 AND schema=$3::jsonb`, tool.Schema, tool.Key, before); err != nil {
				return err
			}
		}
	}
	if err = pg.SetSetting(flag, "true"); err != nil {
		return err
	}
	log.Printf("[localization] English built-in defaults migrated")
	return nil
}
