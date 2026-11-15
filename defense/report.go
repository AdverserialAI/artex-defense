package defense

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
)

func RenderRemediation(bundle Bundle) string {
	var b strings.Builder
	b.WriteString("# Defensive remediation review\n\n")
	b.WriteString("This bundle is evidence for human review. It is not an approval, commit, push, merge, or deployment.\n\n")
	fmt.Fprintf(&b, "- Task: `%s`\n- Repository: `%s`\n- Profile: `%s`\n\n", bundle.Task.ID, bundle.Task.RepositoryRoot, Profile)
	b.WriteString("## Findings\n\n")
	for _, finding := range bundle.Assessment.Findings {
		fmt.Fprintf(&b, "### %s\n\n- Status: **%s**\n- Severity / confidence: %s / %s\n- Reachability: %s\n- Fingerprint: `%s`\n", finding.Title, finding.Status, finding.Severity, finding.Confidence, finding.Reachability, finding.Fingerprint)
		fmt.Fprintf(&b, "- Source: %s\n- Sink: %s\n\n", finding.Source, finding.Sink)
		b.WriteString("Evidence:\n\n")
		for _, evidence := range finding.Evidence {
			fmt.Fprintf(&b, "- `%s:%d`: %s\n", evidence.Path, evidence.StartLine, evidence.Description)
			if evidence.Snippet != "" {
				fmt.Fprintf(&b, "\n  ```text\n  %s\n  ```\n", strings.ReplaceAll(evidence.Snippet, "```", "'''"))
			}
		}
		b.WriteString("\nAssumptions:\n\n- Static and dependency evidence is not proof beyond the recorded reachability assessment.\n\nUnresolved questions:\n\n")
		if finding.Status == FindingNeedsReview {
			b.WriteString("- A human must establish reachability and patchability before remediation.\n\n")
		} else {
			b.WriteString("- Confirm the patch is appropriate for the repository's supported API and deployment context.\n\n")
		}
	}
	if bundle.Patch != nil {
		p := bundle.Patch
		fmt.Fprintf(&b, "## Patch proposal\n\n- ID: `%s`\n- Status: **%s**\n- Risk: %s\n- Summary: %s\n- Rationale: %s\n- Files: `%s`\n- Regression tests: `%s`\n\n", p.ID, p.Status, p.RiskLevel, p.Summary, p.Rationale, strings.Join(p.FilesChanged, "`, `"), strings.Join(p.RegressionTests, "`, `"))
		b.WriteString("### Changed behavior\n\nDangerous prototype-mutating keys are rejected; ordinary nested merge behavior is preserved and tested.\n\n### Unified diff\n\n```diff\n" + p.UnifiedDiff + "\n```\n\n")
	}
	if bundle.Validation != nil {
		v := bundle.Validation
		fmt.Fprintf(&b, "## Independent validation\n\n- Status: **%s**\n- Environment digest: `%s`\n- Baseline regression: %s (a failure is expected and required)\n- Patched tests: %s\n- Build: %s\n- Scanner: %s\n\n", v.Status, v.EnvironmentDigest, v.BaselineTestResult.Status, v.PatchedTestResult.Status, v.BuildResult.Status, v.ScannerResult.Status)
		if v.FailureReason != "" {
			b.WriteString("Failure: " + v.FailureReason + "\n\n")
		}
		b.WriteString("### Actual commands and output\n\n")
		for i, command := range v.Commands {
			fmt.Fprintf(&b, "#### %d. `%s`\n\n- Kind: %s\n- Exit code: %d\n- Duration: %s\n- Started: %s\n\n```text\n%s\n```\n\n", i+1, strings.Join(command.Command, " "), command.Kind, command.ExitCode, command.Duration, command.StartedAt.Format("2006-01-02T15:04:05Z07:00"), strings.ReplaceAll(command.Output, "```", "'''"))
		}
	}
	b.WriteString("## Residual risk\n\n- Validation covers the recorded commands and fixture behavior, not every possible caller or deployment.\n- Dependency advisories and uncertain static observations remain `needs_review` until independently assessed.\n- Human review is required even after all commands pass.\n\n## Rollback\n\nDo not apply or roll back changes from the defense run itself. After a reviewer applies `patch.diff` to a separate branch, the reverse diff is the rollback artifact:\n\n```sh\ngit apply -R patch.diff\n```\n\nIf additional changes were made afterward, revert the reviewed commit through the repository's normal review process instead.\n")
	return b.String()
}

func ExportBundle(bundle Bundle, outputDir string) error {
	if bundle.Task.RepositoryRoot == "" || outputDir == "" {
		return errors.New("task repository and output directory are required")
	}
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(bundle.Task.RepositoryRoot, absOut)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("report output must be outside the audited source repository")
	}
	if err := os.MkdirAll(absOut, 0o700); err != nil {
		return err
	}
	bundle.Report = RenderRemediation(bundle)
	if bundle.Validation != nil {
		bundle.Validation.Artifacts = []string{"patch.diff", "validation.json", "findings.json", "remediation.md", "bundle.json"}
	}
	files := map[string][]byte{"remediation.md": []byte(bundle.Report)}
	addJSON := func(name string, value any) error {
		data, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			files[name] = append(data, '\n')
		}
		return err
	}
	if err := addJSON("task.json", bundle.Task); err != nil {
		return err
	}
	if err := addJSON("findings.json", bundle.Assessment); err != nil {
		return err
	}
	if err := addJSON("bundle.json", bundle); err != nil {
		return err
	}
	if bundle.Patch != nil {
		files["patch.diff"] = []byte(bundle.Patch.UnifiedDiff)
		if err := addJSON("patch.json", bundle.Patch); err != nil {
			return err
		}
	}
	if bundle.Validation != nil {
		if err := addJSON("validation.json", bundle.Validation); err != nil {
			return err
		}
	}
	manifest := "# ARTEX Defense bundle SHA-256\n"
	for name, data := range files {
		sum := sha256.Sum256(data)
		manifest += hex.EncodeToString(sum[:]) + "  " + name + "\n"
	}
	files["SHA256SUMS"] = []byte(manifest)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(absOut, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func RenderReviewHTML(bundle Bundle) (string, error) {
	const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>ARTEX Defense review</title><style>body{font:14px system-ui;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#182018}pre{overflow:auto;background:#101510;color:#e5eee5;padding:1rem;border-radius:6px}.status{font-weight:700}section{border-top:1px solid #ccd5cc;margin-top:2rem;padding-top:1rem}</style></head><body><h1>ARTEX Defense review</h1><p>Evidence only. Human review is required; no commit, push, merge, or deployment has occurred.</p><pre>{{.Report}}</pre></body></html>`
	tmpl, err := template.New("review").Parse(page)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	bundle.Report = RenderRemediation(bundle)
	if err := tmpl.Execute(&b, bundle); err != nil {
		return "", err
	}
	return b.String(), nil
}
