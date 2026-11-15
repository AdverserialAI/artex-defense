package defense

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrototypeAndUncertainStaticFindings(t *testing.T) {
	for _, fixture := range []struct {
		path, rule string
		status     FindingStatus
	}{{"testdata/prototype-pollution", RulePrototype, FindingConfirmed}, {"testdata/uncertain", RuleDynamicSet, FindingNeedsReview}} {
		task := DefaultTask(fixture.path)
		task.ID = "task"
		repo, err := NewRepository(task)
		if err != nil {
			t.Fatal(err)
		}
		findings, _, err := ScanStatic(repo, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 1 || findings[0].RuleID != fixture.rule || findings[0].Status != fixture.status {
			t.Fatalf("%s: %+v", fixture.path, findings)
		}
	}
}

func TestPatchedPrototypeIsNotReported(t *testing.T) {
	content := `function merge(target, source) {
  for (const key in source) {
    if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
    target[key] = source[key];
  }
}`
	findings := scanJavaScript("task", "merge.js", content)
	if len(findings) != 0 {
		t.Fatalf("patched code reported: %+v", findings)
	}
}

func TestGuardDoesNotHideUnguardedAssignment(t *testing.T) {
	content := `function merge(target, source) {
  for (const key in source) {
    if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
    target[key] = source[key];
  }
}
function assign(target, key, value) {
  target[key] = value;
}`
	findings := scanJavaScript("task", "mixed.js", content)
	if len(findings) != 1 || findings[0].Status != FindingNeedsReview || findings[0].Evidence[0].StartLine != 8 {
		t.Fatalf("unguarded assignment hidden: %+v", findings)
	}
}

func TestNormalizeNPMAudit(t *testing.T) {
	raw := `{"vulnerabilities":{"example":{"name":"example","severity":"high","range":"<1.0.1","nodes":["node_modules/example"],"via":[{"source":123,"name":"example","title":"Example advisory","severity":"high","range":"<1.0.1","url":"https://advisories.example/123"}]}}}`
	findings, err := NormalizeNPMAudit("task", raw)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings %+v err %v", findings, err)
	}
	if findings[0].Status != FindingNeedsReview || findings[0].AffectedPackage != "example" || findings[0].RuleID != RuleNPMVuln+"_123" {
		t.Fatalf("finding %+v", findings[0])
	}
	if _, err := json.Marshal(findings); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeGoVulncheck(t *testing.T) {
	raw := `{"osv":{"id":"GO-TEST-1","summary":"Test vulnerability","affected":[{"package":{"name":"example.com/pkg"}}]}}
{"finding":{"osv":"GO-TEST-1","fixed_version":"v1.2.3","trace":[{"function":"main.call","position":{"filename":"main.go","line":9}}]}}
`
	findings, err := NormalizeGoVulncheck("task", raw)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings %+v err %v", findings, err)
	}
	if findings[0].Status != FindingConfirmed || findings[0].AffectedPackage != "example.com/pkg" || findings[0].Evidence[0].Data["fixed_version"] != "v1.2.3" {
		t.Fatalf("finding %+v", findings[0])
	}
}

func TestSecretScannerNeverRetainsValue(t *testing.T) {
	repo, _ := testRepository(t)
	findings, _, err := ScanSecrets(repo, "task")
	if err != nil || len(findings) == 0 {
		t.Fatalf("findings %+v err %v", findings, err)
	}
	for _, finding := range findings {
		for _, evidence := range finding.Evidence {
			if strings.Contains(evidence.Snippet, "supersecret") {
				t.Fatal("raw secret retained")
			}
		}
	}
}
