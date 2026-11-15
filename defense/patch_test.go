package defense

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedDiffRoundTrip(t *testing.T) {
	root := t.TempDir()
	original := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	updated := "one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nNINE\nten\nnew\n"
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := GenerateUnifiedDiff([]FileChange{{Path: "file.txt", OldContent: original, NewContent: updated}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUnifiedDiff(root, diff); err != nil {
		t.Fatalf("apply: %v\n%s", err, diff)
	}
	actual, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(actual) != updated {
		t.Fatalf("got %q want %q (%v)", actual, updated, err)
	}
	if err := ApplyUnifiedDiff(root, diff); err == nil {
		t.Fatal("diff applied twice without detecting stale context")
	}
}

func TestUnifiedDiffNewFile(t *testing.T) {
	root := t.TempDir()
	content := "first\nsecond\n"
	diff, err := GenerateUnifiedDiff([]FileChange{{Path: "new.txt", NewContent: content}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUnifiedDiff(root, diff); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "new.txt"))
	if string(actual) != content {
		t.Fatalf("got %q", actual)
	}
}

func TestPatchPathSizeAndCoverageConstraints(t *testing.T) {
	_, task := testRepository(t)
	patch := &PatchProposal{Changes: []FileChange{{Path: "../escape.js", NewContent: "bad"}, {Path: "index.regression.test.js", NewContent: "test", RegressionTest: true}}}
	if err := ValidatePatch(task, patch); err == nil {
		t.Fatal("path traversal accepted")
	}
	patch.Changes[0].Path = "index.js"
	patch.Changes[1].RegressionTest = false
	if err := ValidatePatch(task, patch); err == nil {
		t.Fatal("missing regression test accepted")
	}
	patch.Changes[1].RegressionTest = true
	task.Limits.MaxChangedLines = 1
	if err := ValidatePatch(task, patch); err == nil {
		t.Fatal("oversized patch accepted")
	}
}

func TestPrototypePatchDoesNotMutateSource(t *testing.T) {
	root := filepath.Join("testdata", "prototype-pollution")
	task := DefaultTask(root)
	task.ID = "fixture"
	task.AllowedWritePaths = []string{"merge.js", "merge.regression.test.js"}
	coordinator, err := NewCoordinator(task)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(coordinator.Repository.Root, "merge.js"))
	assessment, err := coordinator.Audit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var finding *DefenseFinding
	for i := range assessment.Findings {
		if assessment.Findings[i].RuleID == RulePrototype {
			finding = &assessment.Findings[i]
		}
	}
	if finding == nil || finding.Status != FindingConfirmed || !finding.Patchable {
		t.Fatalf("finding: %+v", assessment.Findings)
	}
	proposal, err := ProposePatch(coordinator.Repository, coordinator.Task, *finding)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(coordinator.Repository.Root, "merge.js"))
	if string(before) != string(after) {
		t.Fatal("source working tree mutated")
	}
	if !strings.Contains(proposal.UnifiedDiff, "__proto__") || !strings.Contains(proposal.UnifiedDiff, "merge.regression.test.js") {
		t.Fatalf("diff: %s", proposal.UnifiedDiff)
	}
	if len(proposal.RegressionTests) != 1 {
		t.Fatalf("regression tests: %v", proposal.RegressionTests)
	}
}
