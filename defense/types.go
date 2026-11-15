// Package defense implements evidence-backed, local-repository remediation.
package defense

import (
	"errors"
	"fmt"
	"time"
)

const (
	SchemaVersion  = "defense.artex/v1"
	Profile        = "defense"
	GeneratedBy    = "artex-defense"
	RulePrototype  = "JS_PROTOTYPE_POLLUTION"
	RuleDynamicSet = "JS_DYNAMIC_PROPERTY_ASSIGNMENT"
	RuleSecret     = "SECRET_EXPOSURE"
	RuleConfig     = "INSECURE_CONFIGURATION"
	RuleGoVuln     = "GO_VULNERABILITY"
	RuleNPMVuln    = "NPM_VULNERABILITY"
)

type TaskStatus string

const (
	TaskDraft        TaskStatus = "draft"
	TaskAuditing     TaskStatus = "auditing"
	TaskPatchPending TaskStatus = "patch_pending"
	TaskValidating   TaskStatus = "validating"
	TaskNeedsReview  TaskStatus = "needs_review"
	TaskCompleted    TaskStatus = "completed"
	TaskRejected     TaskStatus = "rejected"
	TaskFailed       TaskStatus = "failed"
)

type FindingStatus string

const (
	FindingNeedsReview  FindingStatus = "needs_review"
	FindingConfirmed    FindingStatus = "confirmed"
	FindingPatchPending FindingStatus = "patch_pending"
	FindingValidated    FindingStatus = "validated"
	FindingRejected     FindingStatus = "rejected"
	FindingDeferred     FindingStatus = "deferred"
)

type PatchStatus string

const (
	PatchDraft     PatchStatus = "draft"
	PatchVerifying PatchStatus = "verifying"
	PatchAccepted  PatchStatus = "accepted"
	PatchRejected  PatchStatus = "rejected"
)

type ValidationStatus string

const (
	ValidationPassed  ValidationStatus = "passed"
	ValidationFailed  ValidationStatus = "failed"
	ValidationBlocked ValidationStatus = "blocked"
)

type ResourceLimits struct {
	MaxFileBytes     int64     `json:"max_file_bytes"`
	MaxContentBytes  int       `json:"max_content_bytes"`
	MaxSearchResults int       `json:"max_search_results"`
	MaxFilesChanged  int       `json:"max_files_changed"`
	MaxChangedLines  int       `json:"max_changed_lines"`
	TimeoutSeconds   int       `json:"timeout_seconds"`
	MemoryMB         int       `json:"memory_mb"`
	CPU              Millicpus `json:"cpu_millicpus"`
	PIDs             int       `json:"pids"`
	OutputBytes      int       `json:"output_bytes"`
}

type Millicpus int

type ValidationPolicy struct {
	Image                  string   `json:"image"`
	NetworkAllowed         bool     `json:"network_allowed"`
	RequireBaselineFailure bool     `json:"require_baseline_failure"`
	CommandAllowlist       []string `json:"command_allowlist"`
	ScannerCommand         []string `json:"scanner_command,omitempty"`
}

type DefenseTask struct {
	SchemaVersion     string           `json:"schema_version"`
	ID                string           `json:"id"`
	Profile           string           `json:"profile"`
	RepositoryRoot    string           `json:"repository_root"`
	DefaultBranch     string           `json:"default_branch,omitempty"`
	Scope             []string         `json:"scope"`
	Constraints       []string         `json:"constraints"`
	AllowedReadPaths  []string         `json:"allowed_read_paths"`
	AllowedWritePaths []string         `json:"allowed_write_paths"`
	Limits            ResourceLimits   `json:"limits"`
	Validation        ValidationPolicy `json:"validation"`
	Status            TaskStatus       `json:"status"`
	CreatedAt         time.Time        `json:"created_at"`
}

type Evidence struct {
	Kind        string            `json:"kind"`
	Path        string            `json:"path,omitempty"`
	StartLine   int               `json:"start_line,omitempty"`
	EndLine     int               `json:"end_line,omitempty"`
	Snippet     string            `json:"snippet,omitempty"`
	Description string            `json:"description"`
	Data        map[string]string `json:"data,omitempty"`
	RawLocation string            `json:"raw_location,omitempty"`
}

type DefenseFinding struct {
	SchemaVersion   string        `json:"schema_version"`
	ID              string        `json:"id"`
	TaskID          string        `json:"task_id"`
	Fingerprint     string        `json:"fingerprint"`
	SourceScanner   string        `json:"source_scanner"`
	ScannerVersion  string        `json:"scanner_version"`
	RuleID          string        `json:"rule_id"`
	Title           string        `json:"title"`
	Severity        string        `json:"severity"`
	Confidence      string        `json:"confidence"`
	AffectedFiles   []string      `json:"affected_files"`
	AffectedPackage string        `json:"affected_package,omitempty"`
	AffectedVersion string        `json:"affected_version,omitempty"`
	Source          string        `json:"source"`
	Sink            string        `json:"sink"`
	Reachability    string        `json:"reachability"`
	Patchable       bool          `json:"patchable"`
	Evidence        []Evidence    `json:"evidence"`
	Status          FindingStatus `json:"status"`
	RejectionReason string        `json:"rejection_reason,omitempty"`
}

type FileChange struct {
	Path           string `json:"path"`
	OldContent     string `json:"old_content"`
	NewContent     string `json:"new_content"`
	RegressionTest bool   `json:"regression_test,omitempty"`
}

type PatchProposal struct {
	SchemaVersion   string       `json:"schema_version"`
	ID              string       `json:"id"`
	FindingID       string       `json:"finding_id"`
	Summary         string       `json:"summary"`
	Rationale       string       `json:"rationale"`
	FilesChanged    []string     `json:"files_changed"`
	UnifiedDiff     string       `json:"unified_diff"`
	RegressionTests []string     `json:"regression_tests"`
	RiskLevel       string       `json:"risk_level"`
	Status          PatchStatus  `json:"status"`
	Changes         []FileChange `json:"changes"`
	GeneratedBy     string       `json:"generated_by"`
	ReviewedBy      string       `json:"reviewed_by,omitempty"`
	RejectionReason string       `json:"rejection_reason,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
}

type Command struct {
	Name     string        `json:"name"`
	Args     []string      `json:"args"`
	Kind     string        `json:"kind"`
	Required bool          `json:"required"`
	Baseline bool          `json:"baseline"`
	Timeout  time.Duration `json:"timeout"`
}

type CommandResult struct {
	Command         []string      `json:"command"`
	Kind            string        `json:"kind"`
	Environment     string        `json:"environment"`
	ExitCode        int           `json:"exit_code"`
	Duration        time.Duration `json:"duration"`
	Output          string        `json:"output"`
	OutputTruncated bool          `json:"output_truncated"`
	StartedAt       time.Time     `json:"started_at"`
	TimedOut        bool          `json:"timed_out"`
}

type CheckResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Evidence []int  `json:"evidence_command_indexes"`
}

type ValidationRun struct {
	SchemaVersion      string           `json:"schema_version"`
	ID                 string           `json:"id"`
	PatchID            string           `json:"patch_id"`
	EnvironmentDigest  string           `json:"environment_digest"`
	Commands           []CommandResult  `json:"commands"`
	ExitCodes          []int            `json:"exit_codes"`
	Durations          []time.Duration  `json:"durations"`
	Logs               []string         `json:"logs"`
	Artifacts          []string         `json:"artifacts"`
	BaselineTestResult CheckResult      `json:"baseline_test_result"`
	PatchedTestResult  CheckResult      `json:"patched_test_result"`
	BuildResult        CheckResult      `json:"build_result"`
	ScannerResult      CheckResult      `json:"scanner_result"`
	Status             ValidationStatus `json:"status"`
	FailureReason      string           `json:"failure_reason,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
}

type Assessment struct {
	SchemaVersion string           `json:"schema_version"`
	TaskID        string           `json:"task_id"`
	Findings      []DefenseFinding `json:"findings"`
	Scans         []ScanRecord     `json:"scans"`
}

type Bundle struct {
	Task       DefenseTask    `json:"task"`
	Assessment Assessment     `json:"assessment"`
	Patch      *PatchProposal `json:"patch,omitempty"`
	Validation *ValidationRun `json:"validation,omitempty"`
	Report     string         `json:"report"`
}

func (r ValidationRun) EvidenceComplete() bool {
	if r.Status != ValidationPassed || r.EnvironmentDigest == "" || len(r.Commands) == 0 {
		return false
	}
	for _, c := range r.Commands {
		if c.Environment == "" || c.StartedAt.IsZero() || c.Duration < 0 || c.ExitCode != 0 && c.Kind != "baseline_regression" {
			return false
		}
	}
	return r.PatchedTestResult.Status == "passed" && r.BuildResult.Status == "passed" && r.ScannerResult.Status == "passed"
}

func (f *DefenseFinding) Transition(next FindingStatus, validation *ValidationRun) error {
	if next == FindingValidated {
		if validation == nil || !validation.EvidenceComplete() {
			return errors.New("validated status requires complete passing command evidence")
		}
		if f.Status != FindingPatchPending {
			return fmt.Errorf("cannot validate finding from %s", f.Status)
		}
		f.Status = next
		return nil
	}
	allowed := false
	switch f.Status {
	case FindingNeedsReview:
		allowed = next == FindingConfirmed || next == FindingRejected || next == FindingDeferred
	case FindingConfirmed:
		allowed = next == FindingPatchPending || next == FindingRejected || next == FindingDeferred
	case FindingPatchPending:
		allowed = next == FindingRejected || next == FindingDeferred || next == FindingNeedsReview
	case FindingValidated, FindingRejected, FindingDeferred:
		allowed = next == FindingNeedsReview
	}
	if !allowed {
		return fmt.Errorf("invalid finding transition %s -> %s", f.Status, next)
	}
	f.Status = next
	return nil
}

func AcceptPatch(p *PatchProposal, validation *ValidationRun, reviewer string) error {
	if p == nil || validation == nil || validation.PatchID != p.ID || !validation.EvidenceComplete() {
		return errors.New("patch acceptance requires passing independent validation for this patch")
	}
	if reviewer == "" || reviewer == GeneratedBy || reviewer == p.GeneratedBy {
		return errors.New("an independent human reviewer is required")
	}
	if p.Status != PatchDraft {
		return fmt.Errorf("patch cannot be accepted from %s", p.Status)
	}
	p.ReviewedBy = reviewer
	p.Status = PatchAccepted
	return nil
}
