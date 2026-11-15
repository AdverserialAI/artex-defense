package defense

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Coordinator struct {
	Task       DefenseTask
	Repository *Repository
	Executor   Executor
}

func NewCoordinator(task DefenseTask) (*Coordinator, error) {
	if err := ValidateTask(&task); err != nil {
		return nil, err
	}
	repo, err := NewRepository(task)
	if err != nil {
		return nil, err
	}
	return &Coordinator{Task: task, Repository: repo}, nil
}

func (c *Coordinator) Audit(ctx context.Context) (Assessment, error) {
	assessment := Assessment{SchemaVersion: SchemaVersion, TaskID: c.Task.ID}
	static, staticRecord, err := ScanStatic(c.Repository, c.Task.ID)
	if err != nil {
		return assessment, err
	}
	assessment.Scans = append(assessment.Scans, staticRecord)
	assessment.Findings = append(assessment.Findings, static...)
	configuration, configRecord, err := ScanConfiguration(c.Repository, c.Task.ID, nil)
	if err != nil {
		return assessment, err
	}
	assessment.Scans = append(assessment.Scans, configRecord)
	assessment.Findings = append(assessment.Findings, configuration...)
	secrets, secretRecord, err := ScanSecrets(c.Repository, c.Task.ID)
	if err != nil {
		return assessment, err
	}
	assessment.Scans = append(assessment.Scans, secretRecord)
	assessment.Findings = append(assessment.Findings, secrets...)
	dependency := DependencyScanner{Repository: c.Repository, NetworkAllowed: c.Task.Validation.NetworkAllowed, Timeout: timeoutFor(c.Task), OutputLimit: c.Task.Limits.OutputBytes}
	if manifestExists(c.Repository, "go.mod") {
		findings, record, err := dependency.ScanGo(ctx, c.Task.ID)
		assessment.Scans = append(assessment.Scans, record)
		if err != nil {
			return assessment, fmt.Errorf("Go dependency scan: %w", err)
		}
		assessment.Findings = append(assessment.Findings, findings...)
	}
	if manifestExists(c.Repository, "package-lock.json") {
		findings, record, err := dependency.ScanNPM(ctx, c.Task.ID)
		assessment.Scans = append(assessment.Scans, record)
		if err != nil {
			return assessment, fmt.Errorf("npm dependency scan: %w", err)
		}
		assessment.Findings = append(assessment.Findings, findings...)
	}
	assessment.Findings = DeduplicateFindings(assessment.Findings)
	return assessment, nil
}

func timeoutFor(task DefenseTask) time.Duration {
	return time.Duration(task.Limits.TimeoutSeconds) * time.Second
}

func (c *Coordinator) Run(ctx context.Context) (Bundle, error) {
	bundle := Bundle{Task: c.Task}
	c.Task.Status = TaskAuditing
	assessment, err := c.Audit(ctx)
	bundle.Task = c.Task
	bundle.Assessment = assessment
	if err != nil {
		c.Task.Status = TaskFailed
		bundle.Task = c.Task
		return bundle, err
	}
	index := -1
	for i := range assessment.Findings {
		if assessment.Findings[i].Status == FindingConfirmed && assessment.Findings[i].Patchable {
			index = i
			break
		}
	}
	if index < 0 {
		c.Task.Status = TaskNeedsReview
		bundle.Task = c.Task
		bundle.Report = RenderRemediation(bundle)
		return bundle, nil
	}
	c.Task.Status = TaskPatchPending
	finding := &assessment.Findings[index]
	patch, err := ProposePatch(c.Repository, c.Task, *finding)
	if err != nil {
		finding.Status, finding.RejectionReason = FindingNeedsReview, err.Error()
		c.Task.Status = TaskNeedsReview
		bundle.Task, bundle.Assessment = c.Task, assessment
		bundle.Report = RenderRemediation(bundle)
		return bundle, nil
	}
	if err := finding.Transition(FindingPatchPending, nil); err != nil {
		return bundle, err
	}
	bundle.Patch = patch
	c.Task.Status = TaskValidating
	bundle.Task, bundle.Assessment = c.Task, assessment
	validator := Validator{Task: c.Task, Repository: c.Repository, Executor: c.Executor}
	validation, validationErr := validator.Validate(ctx, patch)
	bundle.Validation = &validation
	if validationErr != nil {
		c.Task.Status = TaskFailed
		bundle.Task, bundle.Assessment = c.Task, assessment
		bundle.Report = RenderRemediation(bundle)
		return bundle, validationErr
	}
	if validation.Status != ValidationPassed {
		patch.Status, patch.RejectionReason = PatchRejected, validation.FailureReason
		_ = finding.Transition(FindingRejected, nil)
		finding.RejectionReason = validation.FailureReason
		c.Task.Status = TaskRejected
	} else {
		if err := finding.Transition(FindingValidated, &validation); err != nil {
			return bundle, err
		}
		c.Task.Status = TaskCompleted
	}
	bundle.Task, bundle.Assessment = c.Task, assessment
	bundle.Report = RenderRemediation(bundle)
	return bundle, nil
}

func ReviewBundle(bundle *Bundle, reviewer string) error {
	if bundle == nil || bundle.Patch == nil || bundle.Validation == nil {
		return errors.New("bundle has no validated patch")
	}
	if err := AcceptPatch(bundle.Patch, bundle.Validation, reviewer); err != nil {
		return err
	}
	bundle.Report = RenderRemediation(*bundle)
	return nil
}
