package defense

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Executor interface {
	EnvironmentDigest(context.Context) (string, error)
	Execute(context.Context, string, Command, ResourceLimits, bool) CommandResult
}

type DockerExecutor struct {
	Image       string
	OutputLimit int
}

func (d DockerExecutor) EnvironmentDigest(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", d.Image, "--format", "{{.Id}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local validation image is unavailable (pulling is not implicit): %w", err)
	}
	digest := strings.TrimSpace(string(output))
	if digest == "" {
		return "", errors.New("validation image has no digest")
	}
	return digest, nil
}

func (d DockerExecutor) Execute(ctx context.Context, checkout string, command Command, limits ResourceLimits, networkAllowed bool) CommandResult {
	started := time.Now().UTC()
	result := CommandResult{Command: append([]string{command.Name}, command.Args...), Kind: command.Kind, Environment: "docker:" + d.Image, StartedAt: started, ExitCode: -1}
	containerName := "artex-defense-" + stableID(command.Name, started.Format(time.RFC3339Nano))
	args := []string{"run", "--rm", "--name", containerName, "--user", "65532:65532", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp:rw,nosuid,size=256m", "--memory", strconv.Itoa(limits.MemoryMB) + "m", "--cpus", fmt.Sprintf("%.3f", float64(limits.CPU)/1000), "--pids-limit", strconv.Itoa(limits.PIDs), "--network", "none", "--env", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "--env", "HOME=/tmp", "--env", "GOCACHE=/tmp/go-cache", "--env", "GOPATH=/tmp/go", "--env", "npm_config_cache=/tmp/npm-cache", "--volume", checkout + ":/workspace:rw", "--workdir", "/workspace", d.Image, command.Name}
	args = append(args, command.Args...)
	if networkAllowed {
		for i, arg := range args {
			if arg == "none" && args[i-1] == "--network" {
				args[i] = "bridge"
				break
			}
		}
	}
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = time.Duration(limits.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "--force", containerName).Run()
	}()
	buffer := &cappedBuffer{limit: limits.OutputBytes}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	err := cmd.Run()
	result.Duration = time.Since(started)
	result.Output, _ = RedactSecrets(buffer.String())
	result.OutputTruncated = buffer.truncated
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut, result.ExitCode = true, 124
		result.Output += "\nvalidation command timed out"
		return result
	}
	if err == nil {
		result.ExitCode = 0
	} else if exit, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exit.ExitCode()
	} else {
		result.Output += "\n" + err.Error()
	}
	return result
}

type Validator struct {
	Task       DefenseTask
	Repository *Repository
	Executor   Executor
}

func (v Validator) Validate(ctx context.Context, patch *PatchProposal) (run ValidationRun, err error) {
	run = ValidationRun{SchemaVersion: SchemaVersion, ID: timestampID("validation"), Status: ValidationBlocked, CreatedAt: time.Now().UTC()}
	if patch == nil {
		return run, errors.New("patch is required")
	}
	run.PatchID = patch.ID
	if patch.Status != PatchDraft && patch.Status != PatchVerifying {
		return run, errors.New("only a draft patch can be validated")
	}
	if err := ValidatePatch(v.Task, patch); err != nil {
		run.FailureReason = err.Error()
		return run, err
	}
	if patch.UnifiedDiff == "" {
		run.FailureReason = "missing unified diff"
		return run, errors.New(run.FailureReason)
	}
	if v.Executor == nil {
		v.Executor = DockerExecutor{Image: v.Task.Validation.Image}
	}
	digest, err := v.Executor.EnvironmentDigest(ctx)
	if err != nil {
		run.FailureReason = err.Error()
		return run, err
	}
	run.EnvironmentDigest = digest
	plan, err := v.commandPlan(patch)
	if err != nil {
		run.FailureReason = err.Error()
		return run, err
	}
	for _, command := range plan {
		if err := ValidateCommand(append([]string{command.Name}, command.Args...), v.Task.Validation.CommandAllowlist); err != nil {
			run.FailureReason = err.Error()
			return run, err
		}
	}
	tempRoot, err := validationTempRoot()
	if err != nil {
		return run, err
	}
	parent, err := os.MkdirTemp(tempRoot, "run-")
	if err != nil {
		return run, err
	}
	defer os.RemoveAll(parent)
	if err := os.Chmod(parent, 0o711); err != nil {
		return run, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return run, err
	}
	baseline := filepath.Join(parent, "baseline")
	patched := filepath.Join(parent, "patched")
	if err := copyCheckout(v.Repository.Root, baseline); err != nil {
		run.FailureReason = err.Error()
		return run, err
	}
	if err := copyCheckout(v.Repository.Root, patched); err != nil {
		run.FailureReason = err.Error()
		return run, err
	}
	var regressionChanges []FileChange
	for _, change := range patch.Changes {
		if change.RegressionTest {
			regressionChanges = append(regressionChanges, change)
		}
	}
	baselineDiff, err := GenerateUnifiedDiff(regressionChanges)
	if err != nil || baselineDiff == "" {
		run.FailureReason = "could not prepare unpatched baseline regression test"
		return run, errors.New(run.FailureReason)
	}
	if err := ApplyUnifiedDiff(baseline, baselineDiff); err != nil {
		run.FailureReason = "baseline preparation failed: " + err.Error()
		return run, err
	}
	if err := ApplyUnifiedDiff(patched, patch.UnifiedDiff); err != nil {
		run.FailureReason = "patch application failed: " + err.Error()
		return run, err
	}
	patch.Status = PatchVerifying
	defer func() {
		if patch.Status == PatchVerifying {
			patch.Status = PatchDraft
		}
	}()
	failed := false
	for _, command := range plan {
		checkout := patched
		if command.Baseline {
			checkout = baseline
		}
		result := v.Executor.Execute(ctx, checkout, command, v.Task.Limits, v.Task.Validation.NetworkAllowed)
		result.Environment += "@" + digest
		index := len(run.Commands)
		run.Commands = append(run.Commands, result)
		run.ExitCodes = append(run.ExitCodes, result.ExitCode)
		run.Durations = append(run.Durations, result.Duration)
		run.Logs = append(run.Logs, result.Output)
		success := result.ExitCode == 0 && !result.TimedOut
		if command.Baseline {
			success = result.ExitCode != 0
		}
		switch command.Kind {
		case "baseline_regression":
			run.BaselineTestResult = mergeCheck(run.BaselineTestResult, "baseline_regression", success, result.ExitCode, index)
			if result.ExitCode != 0 {
				run.BaselineTestResult.Status = "failed"
			}
		case "regression":
			run.PatchedTestResult = mergeCheck(run.PatchedTestResult, "patched_regression", success, result.ExitCode, index)
		case "existing_tests":
			run.PatchedTestResult = mergeCheck(run.PatchedTestResult, "existing_tests", success, result.ExitCode, index)
		case "build", "typecheck":
			run.BuildResult = mergeCheck(run.BuildResult, command.Kind, success, result.ExitCode, index)
		case "scanner":
			run.ScannerResult = mergeCheck(run.ScannerResult, "scanner", success, result.ExitCode, index)
		}
		if command.Required && !success {
			failed = true
		}
	}
	if run.BaselineTestResult.Status == "" {
		run.BaselineTestResult.Status = "not_run"
	}
	if run.PatchedTestResult.Status == "" {
		run.PatchedTestResult.Status = "not_run"
	}
	if run.BuildResult.Status == "" {
		run.BuildResult.Status = "not_run"
	}
	if run.ScannerResult.Status == "" {
		run.ScannerResult.Status = "not_run"
	}
	if v.Task.Validation.RequireBaselineFailure && run.BaselineTestResult.Status != "failed" {
		failed = true
	}
	for _, required := range []CheckResult{run.PatchedTestResult, run.BuildResult, run.ScannerResult} {
		if required.Status != "passed" {
			failed = true
		}
	}
	if failed {
		run.Status = ValidationFailed
		run.FailureReason = "one or more required validation checks failed; inspect actual command output"
		return run, nil
	}
	run.Status = ValidationPassed
	return run, nil
}

func mergeCheck(check CheckResult, name string, success bool, exit, index int) CheckResult {
	if check.Name == "" {
		check.Name = name
		check.Status = "passed"
	} else if check.Name != name {
		check.Name += "," + name
	}
	if !success {
		check.Status = "failed"
	}
	check.ExitCode = exit
	check.Evidence = append(check.Evidence, index)
	return check
}

func (v Validator) commandPlan(patch *PatchProposal) ([]Command, error) {
	timeout := time.Duration(v.Task.Limits.TimeoutSeconds) * time.Second
	var plan []Command
	if len(patch.RegressionTests) == 0 {
		return nil, errors.New("a regression test command plan is required")
	}
	manifest, manifestErr := v.Repository.Manifest("package.json")
	hasNPM := manifestErr == nil && manifest.Ecosystem == "npm"
	if hasNPM {
		for _, test := range patch.RegressionTests {
			plan = append(plan, Command{Name: "node", Args: []string{"--test", test}, Kind: "baseline_regression", Required: true, Baseline: true, Timeout: timeout})
			plan = append(plan, Command{Name: "node", Args: []string{"--test", test}, Kind: "regression", Required: true, Timeout: timeout})
		}
		plan = append(plan, Command{Name: "node", Args: []string{"--test"}, Kind: "existing_tests", Required: true, Timeout: timeout})
		if len(manifest.Scripts["build"]) > 0 {
			plan = append(plan, Command{Name: "npm", Args: []string{"run", "build", "--if-present", "--ignore-scripts"}, Kind: "build", Required: true, Timeout: timeout})
		} else {
			for _, file := range patch.FilesChanged {
				if filepath.Ext(file) == ".js" && !strings.HasSuffix(file, ".test.js") {
					plan = append(plan, Command{Name: "node", Args: []string{"--check", file}, Kind: "build", Required: true, Timeout: timeout})
				}
			}
		}
		scanner := v.Task.Validation.ScannerCommand
		if len(scanner) == 0 {
			scanner = []string{"npm", "audit", "--json", "--package-lock-only", "--ignore-scripts", "--offline"}
		}
		if len(scanner) > 0 {
			plan = append(plan, Command{Name: scanner[0], Args: scanner[1:], Kind: "scanner", Required: true, Timeout: timeout})
		}
	}
	if manifestExists(v.Repository, "go.mod") {
		for _, test := range patch.RegressionTests {
			if !strings.HasSuffix(test, "_test.go") {
				return nil, errors.New("Go regression tests must use _test.go files")
			}
			plan = append(plan, Command{Name: "go", Args: []string{"test", "./..."}, Kind: "baseline_regression", Required: true, Baseline: true, Timeout: timeout})
			plan = append(plan, Command{Name: "go", Args: []string{"test", "./..."}, Kind: "regression", Required: true, Timeout: timeout})
		}
		plan = append(plan, Command{Name: "go", Args: []string{"test", "./..."}, Kind: "existing_tests", Required: true, Timeout: timeout}, Command{Name: "go", Args: []string{"build", "./..."}, Kind: "build", Required: true, Timeout: timeout})
		if len(v.Task.Validation.ScannerCommand) > 0 {
			plan = append(plan, Command{Name: v.Task.Validation.ScannerCommand[0], Args: v.Task.Validation.ScannerCommand[1:], Kind: "scanner", Required: true, Timeout: timeout})
		}
	}
	if len(plan) == 0 {
		return nil, errors.New("no Go or npm validation plan is available")
	}
	return plan, nil
}

func validationTempRoot() (string, error) {
	if root := os.Getenv("ARTEX_DEFENSE_TEMP"); root != "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
		return root, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "artex-defense-validation")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func copyCheckout(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			if err := os.MkdirAll(destination, 0o777); err != nil {
				return err
			}
			return os.Chmod(destination, 0o777)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("validation checkout contains symlink: %s", rel)
		}
		if entry.IsDir() {
			if rel == ".git" || rel == "node_modules" || rel == "dist" || rel == "out" || rel == ".next" {
				return filepath.SkipDir
			}
			dir := filepath.Join(destination, rel)
			if err := os.MkdirAll(dir, 0o777); err != nil {
				return err
			}
			return os.Chmod(dir, 0o777)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if err := os.WriteFile(target, data, 0o666); err != nil {
			return err
		}
		return os.Chmod(target, 0o666)
	})
}
