package defense

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type localTestExecutor struct {
	mu      sync.Mutex
	network []bool
}

func (e *localTestExecutor) EnvironmentDigest(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "node", "--version").Output()
	return "test-node:" + strings.TrimSpace(string(output)), err
}

func (e *localTestExecutor) Execute(ctx context.Context, checkout string, command Command, limits ResourceLimits, network bool) CommandResult {
	e.mu.Lock()
	e.network = append(e.network, network)
	e.mu.Unlock()
	started := time.Now().UTC()
	result := CommandResult{Command: append([]string{command.Name}, command.Args...), Kind: command.Kind, Environment: "isolated-test-process", StartedAt: started, ExitCode: -1}
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = time.Duration(limits.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = checkout
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "npm_config_cache=/tmp/artex-defense-test-npm"}
	buffer := &cappedBuffer{limit: limits.OutputBytes}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	err := cmd.Run()
	result.Duration = time.Since(started)
	result.Output = buffer.String()
	result.OutputTruncated = buffer.truncated
	if ctx.Err() == context.DeadlineExceeded {
		result.ExitCode, result.TimedOut = 124, true
	} else if err == nil {
		result.ExitCode = 0
	} else if exit, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exit.ExitCode()
	}
	return result
}

func fixtureCoordinator(t *testing.T, fixture string) (*Coordinator, *localTestExecutor, string) {
	t.Helper()
	source, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := copyCheckout(source, root); err != nil {
		t.Fatal(err)
	}
	task := DefaultTask(root)
	task.ID = "fixture-e2e"
	task.AllowedWritePaths = []string{"merge.js", "merge.regression.test.js"}
	coordinator, err := NewCoordinator(task)
	if err != nil {
		t.Fatal(err)
	}
	executor := &localTestExecutor{}
	coordinator.Executor = executor
	return coordinator, executor, root
}

func TestEndToEndCorrectPatchAndReview(t *testing.T) {
	coordinator, executor, root := fixtureCoordinator(t, "testdata/prototype-pollution")
	tempParent := t.TempDir()
	t.Setenv("ARTEX_DEFENSE_TEMP", tempParent)
	before := treeDigest(t, root)
	bundle, err := coordinator.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Validation == nil || bundle.Validation.Status != ValidationPassed {
		t.Fatalf("validation: %+v", bundle.Validation)
	}
	if bundle.Validation.BaselineTestResult.Status != "failed" || bundle.Validation.PatchedTestResult.Status != "passed" || bundle.Validation.BuildResult.Status != "passed" || bundle.Validation.ScannerResult.Status != "passed" {
		t.Fatalf("checks: %+v", bundle.Validation)
	}
	if bundle.Assessment.Findings[0].Status != FindingValidated {
		t.Fatalf("finding: %+v", bundle.Assessment.Findings)
	}
	if after := treeDigest(t, root); after != before {
		t.Fatal("audit or validation mutated source tree")
	}
	for _, allowed := range executor.network {
		if allowed {
			t.Fatal("network enabled by default")
		}
	}
	entries, err := os.ReadDir(tempParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary validation resources remain: %v %v", entries, err)
	}
	if err := ReviewBundle(&bundle, GeneratedBy); err == nil {
		t.Fatal("self approval accepted")
	}
	if err := ReviewBundle(&bundle, "human-reviewer"); err != nil {
		t.Fatal(err)
	}
	if bundle.Patch.Status != PatchAccepted {
		t.Fatal("human review not recorded")
	}
	if !strings.Contains(bundle.Report, "Actual commands and output") || !strings.Contains(bundle.Report, "Rollback") {
		t.Fatal("report lacks evidence or rollback")
	}
	output := filepath.Join(t.TempDir(), "bundle")
	if err := ExportBundle(bundle, output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"patch.diff", "findings.json", "validation.json", "remediation.md", "bundle.json", "SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrokenPatchRejectedWithLogs(t *testing.T) {
	coordinator, executor, _ := fixtureCoordinator(t, "testdata/prototype-pollution")
	assessment, err := coordinator.Audit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var finding DefenseFinding
	for _, item := range assessment.Findings {
		if item.RuleID == RulePrototype {
			finding = item
		}
	}
	patch, err := ProposePatch(coordinator.Repository, coordinator.Task, finding)
	if err != nil {
		t.Fatal(err)
	}
	patch.Changes[0].NewContent = patch.Changes[0].OldContent
	patch.UnifiedDiff, err = GenerateUnifiedDiff(patch.Changes)
	if err != nil {
		t.Fatal(err)
	}
	run, err := (Validator{Task: coordinator.Task, Repository: coordinator.Repository, Executor: executor}).Validate(t.Context(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != ValidationFailed || run.PatchedTestResult.Status != "failed" || len(run.Logs) == 0 {
		t.Fatalf("broken patch accepted: %+v", run)
	}
	if finding.Transition(FindingPatchPending, nil) != nil {
		t.Fatal("transition setup")
	}
	if err := finding.Transition(FindingValidated, &run); err == nil {
		t.Fatal("failed validation marked finding validated")
	}
}

func TestMissingRegressionCoverageBlocked(t *testing.T) {
	coordinator, executor, _ := fixtureCoordinator(t, "testdata/prototype-pollution")
	assessment, _ := coordinator.Audit(t.Context())
	var finding DefenseFinding
	for _, item := range assessment.Findings {
		if item.RuleID == RulePrototype {
			finding = item
		}
	}
	patch, err := ProposePatch(coordinator.Repository, coordinator.Task, finding)
	if err != nil {
		t.Fatal(err)
	}
	patch.Changes = patch.Changes[:1]
	patch.RegressionTests = nil
	run, err := (Validator{Task: coordinator.Task, Repository: coordinator.Repository, Executor: executor}).Validate(t.Context(), patch)
	if err == nil || run.Status != ValidationBlocked {
		t.Fatalf("missing test accepted: %+v %v", run, err)
	}
}

func TestUncertainFindingProducesNoPatch(t *testing.T) {
	root := t.TempDir()
	if err := copyCheckout("testdata/uncertain", root); err != nil {
		t.Fatal(err)
	}
	task := DefaultTask(root)
	task.ID = "uncertain"
	coordinator, err := NewCoordinator(task)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := coordinator.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Patch != nil || bundle.Validation != nil || bundle.Task.Status != TaskNeedsReview || len(bundle.Assessment.Findings) != 1 || bundle.Assessment.Findings[0].Status != FindingNeedsReview {
		t.Fatalf("bundle: %+v", bundle)
	}
}

func TestCommandTimeoutProducesEvidence(t *testing.T) {
	executor := &localTestExecutor{}
	result := executor.Execute(t.Context(), t.TempDir(), Command{Name: "node", Args: []string{"-e", "setTimeout(() => {}, 1000)"}, Timeout: 10 * time.Millisecond}, DefaultTask("").Limits, false)
	if !result.TimedOut || result.ExitCode != 124 || result.StartedAt.IsZero() || result.Duration <= 0 {
		t.Fatalf("timeout evidence: %+v", result)
	}
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(filepath.Base(file))
		b.Write(data)
	}
	return stableID(b.String())
}
