package defense

import (
	"strings"
	"testing"
	"time"
)

func TestFindingNormalizationFingerprintAndDeduplication(t *testing.T) {
	base := DefenseFinding{SourceScanner: "scanner", RuleID: "rule", Title: "Finding", Severity: "HIGH", Confidence: "medium", AffectedFiles: []string{"b.js", "a.js", "a.js"}, Source: "input", Sink: "output", Evidence: []Evidence{{Path: "a.js", StartLine: 2, Snippet: `password = "123456789"`}}}
	first, err := NormalizeFinding("task", base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != FindingNeedsReview || first.Severity != "high" || !strings.HasPrefix(first.Fingerprint, "sha256:") {
		t.Fatalf("normalization: %+v", first)
	}
	if strings.Contains(first.Evidence[0].Snippet, "123456789") {
		t.Fatal("evidence was not redacted")
	}
	second, err := NormalizeFinding("task", base)
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("unstable fingerprint: %s %s %v", first.Fingerprint, second.Fingerprint, err)
	}
	second.Severity, second.Confidence, second.Reachability = "critical", "high", "confirmed"
	second.Status = FindingConfirmed
	deduped := DeduplicateFindings([]DefenseFinding{first, second})
	if len(deduped) != 1 || deduped[0].Severity != "critical" || len(deduped[0].Evidence) != 2 {
		t.Fatalf("deduplication: %+v", deduped)
	}
}

func TestFindingTransitionsRequireActualValidation(t *testing.T) {
	finding := DefenseFinding{Status: FindingConfirmed}
	if err := finding.Transition(FindingValidated, nil); err == nil {
		t.Fatal("validated without evidence")
	}
	if err := finding.Transition(FindingPatchPending, nil); err != nil {
		t.Fatal(err)
	}
	run := ValidationRun{Status: ValidationPassed, EnvironmentDigest: "sha256:image", Commands: []CommandResult{{Kind: "regression", Environment: "container", StartedAt: time.Now().UTC(), ExitCode: 0}}, PatchedTestResult: CheckResult{Status: "passed"}, BuildResult: CheckResult{Status: "passed"}, ScannerResult: CheckResult{Status: "passed"}}
	if err := finding.Transition(FindingValidated, &run); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentHumanPatchApproval(t *testing.T) {
	patch := &PatchProposal{ID: "patch", Status: PatchDraft, GeneratedBy: GeneratedBy}
	run := &ValidationRun{PatchID: "patch", Status: ValidationPassed, EnvironmentDigest: "digest", Commands: []CommandResult{{Kind: "regression", Environment: "container", StartedAt: time.Now().UTC()}}, PatchedTestResult: CheckResult{Status: "passed"}, BuildResult: CheckResult{Status: "passed"}, ScannerResult: CheckResult{Status: "passed"}}
	if err := AcceptPatch(patch, run, GeneratedBy); err == nil {
		t.Fatal("patch approved itself")
	}
	if err := AcceptPatch(patch, run, "reviewer@example.com"); err != nil {
		t.Fatal(err)
	}
	if patch.Status != PatchAccepted {
		t.Fatalf("status %s", patch.Status)
	}
}
