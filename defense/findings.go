package defense

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

func NormalizeFinding(taskID string, input DefenseFinding) (DefenseFinding, error) {
	input.SchemaVersion = SchemaVersion
	input.TaskID = taskID
	input.SourceScanner = strings.TrimSpace(input.SourceScanner)
	input.RuleID = strings.ToUpper(strings.TrimSpace(input.RuleID))
	input.Title = strings.TrimSpace(input.Title)
	input.Severity = strings.ToLower(strings.TrimSpace(input.Severity))
	input.Confidence = strings.ToLower(strings.TrimSpace(input.Confidence))
	if input.SourceScanner == "" || input.RuleID == "" || input.Title == "" {
		return input, errors.New("scanner, rule, and title are required")
	}
	switch input.Severity {
	case "critical", "high", "medium", "low", "info":
	default:
		return input, errors.New("invalid finding severity")
	}
	switch input.Confidence {
	case "high", "medium", "low":
	default:
		return input, errors.New("invalid finding confidence")
	}
	if input.ScannerVersion == "" {
		input.ScannerVersion = "unknown"
	}
	if input.Reachability == "" {
		input.Reachability = "unknown"
	}
	if input.Reachability != "confirmed" && input.Reachability != "unknown" && input.Reachability != "not_reached" {
		return input, errors.New("invalid reachability")
	}
	if len(input.Evidence) == 0 {
		return input, errors.New("structured evidence is required")
	}
	for i := range input.Evidence {
		input.Evidence[i].Snippet = redactEvidence(input.Evidence[i].Snippet)
		input.Evidence[i].Description = redactEvidence(input.Evidence[i].Description)
	}
	input.AffectedFiles = cleanFindingPaths(input.AffectedFiles)
	if input.Fingerprint == "" {
		input.Fingerprint = FindingFingerprint(input)
	}
	if input.ID == "" {
		input.ID = stableID("finding", taskID, input.Fingerprint)
	}
	input.Status = FindingNeedsReview
	if input.Reachability == "confirmed" && input.Confidence != "low" {
		input.Status = FindingConfirmed
	}
	return input, nil
}

func cleanFindingPaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = pathClean(p)
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func FindingFingerprint(f DefenseFinding) string {
	files := cleanFindingPaths(f.AffectedFiles)
	firstFile, line := "", 0
	if len(files) > 0 {
		firstFile = files[0]
	}
	if len(f.Evidence) > 0 {
		line = f.Evidence[0].StartLine
	}
	value := strings.Join([]string{f.SourceScanner, f.RuleID, firstFile, f.AffectedPackage, f.Source, f.Sink}, "\x00")
	if firstFile != "" && line > 0 {
		value += "\x00" + f.Evidence[0].Path + "\x00" + strconv.Itoa(line)
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DeduplicateFindings(findings []DefenseFinding) []DefenseFinding {
	byFingerprint := map[string]DefenseFinding{}
	for _, finding := range findings {
		if existing, ok := byFingerprint[finding.Fingerprint]; ok {
			existing.Evidence = append(existing.Evidence, finding.Evidence...)
			if severityRank(finding.Severity) > severityRank(existing.Severity) {
				existing.Severity = finding.Severity
			}
			if confidenceRank(finding.Confidence) > confidenceRank(existing.Confidence) {
				existing.Confidence = finding.Confidence
			}
			if finding.Reachability == "confirmed" {
				existing.Reachability = "confirmed"
			}
			existing.Patchable = existing.Patchable || finding.Patchable
			byFingerprint[finding.Fingerprint] = existing
		} else {
			byFingerprint[finding.Fingerprint] = finding
		}
	}
	out := make([]DefenseFinding, 0, len(byFingerprint))
	for _, finding := range byFingerprint {
		out = append(out, finding)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	default:
		return 1
	}
}
func confidenceRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

func newFinding(taskID, scanner, version, rule, title, severity, confidence string, evidence Evidence) DefenseFinding {
	f, _ := NormalizeFinding(taskID, DefenseFinding{
		SourceScanner: scanner, ScannerVersion: version, RuleID: rule, Title: title,
		Severity: severity, Confidence: confidence, AffectedFiles: []string{evidence.Path},
		Evidence: []Evidence{evidence}, Reachability: "unknown", Status: FindingNeedsReview,
	})
	return f
}

func timestampID(prefix string) string {
	return stableID(prefix, time.Now().UTC().Format(time.RFC3339Nano))
}
