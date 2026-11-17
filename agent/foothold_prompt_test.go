package agent

import (
	"strings"
	"testing"
)

func TestGoalAndExternalPlannerFootholdSemantics(t *testing.T) {
	goalRules := []string{
		"Match goal types to Shell intent semantically",
		"Strong Shell/foothold intent",
		"These words alone do not imply getshell",
		"Classify by the actual vulnerability",
		"WebShell, reverse Shell, and C2 are alternative implementation paths",
	}
	for _, rule := range goalRules {
		if !strings.Contains(goalsDefaultTmpl, rule) {
			t.Errorf("goals prompt missing rule %q", rule)
		}
	}

	plannerRules := []string{
		"For vulnclass=getshell goals",
		"Only a confirmed webshell/reverse_shell/getshell Finding",
		"An attacker-local Shell or started listener is not a target session",
		"alone does not prove getshell",
		"It may satisfy the corresponding rce/file_upload/deserialization/sqli/privilege-verification goal",
		"The same acceptance checks apply before goal_met",
	}
	for _, rule := range plannerRules {
		if !strings.Contains(plannerDefaultTmpl, rule) {
			t.Errorf("external planner prompt missing rule %q", rule)
		}
	}
}

func TestFootholdPromptChangeIsLimitedToExternalPlanner(t *testing.T) {
	const marker = "For vulnclass=getshell goals"
	for name, body := range PlannerDomainSeeds() {
		if strings.Contains(body, marker) {
			t.Errorf("domain planner %q unexpectedly received external-only rule", name)
		}
	}
}
