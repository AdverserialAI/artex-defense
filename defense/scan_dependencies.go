package defense

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ScanRecord struct {
	Scanner      string   `json:"scanner"`
	Version      string   `json:"version"`
	Command      []string `json:"command"`
	RawEvidence  string   `json:"raw_evidence,omitempty"`
	RawLocation  string   `json:"raw_location,omitempty"`
	FindingCount int      `json:"finding_count"`
	Warnings     []string `json:"warnings,omitempty"`
}

type DependencyScanner struct {
	Repository     *Repository
	Timeout        time.Duration
	NetworkAllowed bool
	OutputLimit    int
}

func (s DependencyScanner) ScanGo(ctx context.Context, taskID string) ([]DefenseFinding, ScanRecord, error) {
	record := ScanRecord{Scanner: "govulncheck", Version: "external", Command: []string{"govulncheck", "-json", "./..."}}
	if !s.NetworkAllowed {
		record.Warnings = []string{"blocked: dependency scanning requires explicit network policy"}
		return nil, record, nil
	}
	output, err := s.run(ctx, record.Command)
	record.RawEvidence = output
	if err != nil {
		return nil, record, err
	}
	findings, err := NormalizeGoVulncheck(taskID, output)
	record.FindingCount = len(findings)
	return findings, record, err
}

func (s DependencyScanner) ScanNPM(ctx context.Context, taskID string) ([]DefenseFinding, ScanRecord, error) {
	record := ScanRecord{Scanner: "npm-audit", Version: "external", Command: []string{"npm", "audit", "--json", "--package-lock-only", "--ignore-scripts"}}
	if !s.NetworkAllowed {
		record.Warnings = []string{"blocked: dependency scanning requires explicit network policy"}
		return nil, record, nil
	}
	output, err := s.run(ctx, record.Command)
	record.RawEvidence = output
	if err != nil && strings.TrimSpace(output) == "" {
		return nil, record, err
	}
	findings, normalizeErr := NormalizeNPMAudit(taskID, output)
	record.FindingCount = len(findings)
	if normalizeErr != nil {
		return nil, record, errors.Join(err, normalizeErr)
	}
	return findings, record, nil
}

func (s DependencyScanner) run(ctx context.Context, argv []string) (string, error) {
	if err := ValidateCommand(argv, []string{"go", "node", "npm", "govulncheck"}); err != nil {
		return "", err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = s.Repository.Root
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + osTempDir(), "GOPROXY=https://proxy.golang.org"}
	limit := s.OutputLimit
	if limit <= 0 {
		limit = 1 << 20
	}
	buffer := &cappedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return buffer.String(), fmt.Errorf("scanner timed out after %s", timeout)
	}
	return buffer.String(), err
}

func osTempDir() string { return "/tmp" }

type goOSV struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Affected []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
	} `json:"affected"`
}

func (v goOSV) AffectedPackage() string {
	if len(v.Affected) > 0 {
		return v.Affected[0].Package.Name
	}
	return ""
}

func NormalizeGoVulncheck(taskID, raw string) ([]DefenseFinding, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	osvs := map[string]goOSV{}
	var findings []DefenseFinding
	for decoder.More() {
		var message map[string]json.RawMessage
		if err := decoder.Decode(&message); err != nil {
			return nil, err
		}
		if data := message["osv"]; len(data) > 0 {
			var value goOSV
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, err
			}
			osvs[value.ID] = value
		}
		if data := message["finding"]; len(data) > 0 {
			var value struct {
				OSV          string `json:"osv"`
				FixedVersion string `json:"fixed_version"`
				Trace        []struct {
					Module   string `json:"module"`
					Version  string `json:"version"`
					Package  string `json:"package"`
					Function string `json:"function"`
					Position struct {
						Filename string `json:"filename"`
						Line     int    `json:"line"`
					} `json:"position"`
				} `json:"trace"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, err
			}
			osv := osvs[value.OSV]
			path, line, source := "", 0, "dependency call graph"
			if len(value.Trace) > 0 {
				last := value.Trace[len(value.Trace)-1]
				path, line, source = last.Position.Filename, last.Position.Line, last.Function
			}
			e := Evidence{Kind: "scanner", Path: path, StartLine: line, EndLine: line, Description: "govulncheck reported a reachable vulnerability in the module call graph.", Data: map[string]string{"fixed_version": value.FixedVersion, "osv": value.OSV}, RawLocation: "inline:govulncheck-json"}
			f := newFinding(taskID, "govulncheck", "external", RuleGoVuln+"_"+value.OSV, firstNonEmpty(osv.Summary, "Known Go vulnerability "+value.OSV), "high", "high", e)
			f.AffectedPackage, f.Source = osv.AffectedPackage(), source
			f.Sink = value.OSV
			if len(value.Trace) > 0 {
				f.Reachability, f.Status = "confirmed", FindingConfirmed
			}
			finding, err := NormalizeFinding(taskID, f)
			if err == nil {
				findings = append(findings, finding)
			}
		}
	}
	return DeduplicateFindings(findings), nil
}

func NormalizeNPMAudit(taskID, raw string) ([]DefenseFinding, error) {
	var report struct {
		Vulnerabilities map[string]struct {
			Name, Severity, Range string
			Nodes                 []string          `json:"nodes"`
			Via                   []json.RawMessage `json:"via"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, err
	}
	var findings []DefenseFinding
	for name, vulnerability := range report.Vulnerabilities {
		for _, rawVia := range vulnerability.Via {
			var via struct {
				Source                            int `json:"source"`
				Name, Title, Severity, Range, URL string
			}
			if err := json.Unmarshal(rawVia, &via); err != nil {
				continue
			}
			if via.Title == "" {
				continue
			}
			path := firstNonEmpty(firstString(vulnerability.Nodes), "package-lock.json")
			e := Evidence{Kind: "scanner", Path: path, Description: "npm audit reported a dependency advisory. Runtime reachability requires review.", Data: map[string]string{"advisory": via.URL, "range": via.Range}, RawLocation: "inline:npm-audit-json"}
			severity := via.Severity
			if severity == "" {
				severity = vulnerability.Severity
			}
			f := newFinding(taskID, "npm-audit", "external", RuleNPMVuln+"_"+strconv.Itoa(via.Source), via.Title, severity, "medium", e)
			f.AffectedPackage, f.AffectedVersion = firstNonEmpty(via.Name, name), vulnerability.Range
			f.Source, f.Sink = "package-lock.json:"+name, via.URL
			finding, err := NormalizeFinding(taskID, f)
			if err == nil {
				findings = append(findings, finding)
			}
		}
	}
	return DeduplicateFindings(findings), nil
}

func firstString(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.Buffer.Write(p)
	}
	if len(p) < original {
		b.truncated = true
	}
	return original, nil
}
func (b *cappedBuffer) String() string {
	if b.truncated {
		return b.Buffer.String() + "\n[OUTPUT TRUNCATED]"
	}
	return b.Buffer.String()
}

func manifestExists(repo *Repository, name string) bool {
	_, _, err := repo.resolve(name, false)
	return err == nil
}

func cleanScannerPath(path string) string { return pathClean(filepath.ToSlash(path)) }
