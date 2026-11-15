package defense

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const StaticScannerVersion = "1.0.0"

var (
	functionPattern          = regexp.MustCompile(`(?m)(?:function\s+([A-Za-z_$][\w$]*)\s*\(([^)]*)\)|(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?\(([^)]*)\)\s*=>)`)
	forInPattern             = regexp.MustCompile(`\bfor\s*\(\s*(?:const|let|var)?\s*([A-Za-z_$][\w$]*)\s+in\s+([A-Za-z_$][\w$]*)\s*\)\s*\{`)
	dynamicAssignmentPattern = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\[\s*([A-Za-z_$][\w$]*)\s*\]\s*=`)
)

type jsFunction struct {
	name   string
	params []string
	start  int
	end    int
}

func ScanStatic(repo *Repository, taskID string) ([]DefenseFinding, ScanRecord, error) {
	record := ScanRecord{Scanner: "artex-static", Version: StaticScannerVersion, Command: []string{"repo.search", "prototype-pollution-rules"}}
	files, err := repo.ListFiles([]string{"**/*.js", "**/*.cjs", "**/*.mjs"}, []string{"**/*.min.js", "**/vendor/**", "**/node_modules/**"})
	if err != nil {
		return nil, record, err
	}
	var findings []DefenseFinding
	for _, file := range files {
		content, err := repo.Read(file.Path, 0, 0)
		if err != nil {
			continue
		}
		if content.Truncated {
			record.Warnings = append(record.Warnings, file.Path+": content truncated")
		}
		found := scanJavaScript(taskID, file.Path, content.Content)
		findings = append(findings, found...)
	}
	record.FindingCount = len(findings)
	return DeduplicateFindings(findings), record, nil
}

func scanJavaScript(taskID, name, content string) []DefenseFinding {
	lines := strings.Split(content, "\n")
	functions := findJSFunctions(content, lines)
	guardedLines := map[int]bool{}
	var findings []DefenseFinding
	for _, fn := range functions {
		body := strings.Join(lines[fn.start:fn.end], "\n")
		matches := forInPattern.FindAllStringSubmatchIndex(body, -1)
		for _, match := range matches {
			key, source := body[match[2]:match[3]], body[match[4]:match[5]]
			loopOffset := strings.Count(body[:match[0]], "\n")
			lineIndex := fn.start + loopOffset
			if !containsString(fn.params, source) {
				findings = append(findings, uncertainDynamicFinding(taskID, name, lines, lineIndex, key, source))
				continue
			}
			loopEnd := matchingBrace(body, match[1]-1)
			if loopEnd < 0 {
				loopEnd = len(body)
			}
			loopBody := body[match[0]:loopEnd]
			if strings.Contains(loopBody, "'__proto__'") && strings.Contains(loopBody, "'constructor'") && strings.Contains(loopBody, "'prototype'") {
				if assignment := dynamicAssignmentPattern.FindStringSubmatch(loopBody); assignment != nil {
					guardedLines[lineIndex+strings.Count(loopBody[:strings.Index(loopBody, assignment[0])], "\n")] = true
				}
				continue
			}
			assignment := dynamicAssignmentPattern.FindStringSubmatch(loopBody)
			if assignment == nil || assignment[2] != key {
				findings = append(findings, uncertainDynamicFinding(taskID, name, lines, lineIndex, key, source))
				continue
			}
			target := assignment[1]
			assignmentLine := lineIndex + strings.Count(loopBody[:strings.Index(loopBody, assignment[0])], "\n")
			evidence := Evidence{Kind: "source_to_sink", Path: name, StartLine: lineIndex + 1, EndLine: assignmentLine + 1,
				Snippet: strings.TrimSpace(lines[lineIndex]), Description: fmt.Sprintf("Untrusted keys from parameter %q flow through for-in into %s[%s]. Prototype-mutating keys are not rejected.", source, target, key),
				Data: map[string]string{"function": fn.name, "key_variable": key, "source_variable": source, "target_variable": target, "loop_line": fmt.Sprint(lineIndex + 1), "assignment_line": fmt.Sprint(assignmentLine + 1)},
			}
			finding := newFinding(taskID, "artex-static", StaticScannerVersion, RulePrototype, "Prototype pollution through recursive object merge", "high", "high", evidence)
			finding.Source = fmt.Sprintf("function %s parameter %s", fn.name, source)
			finding.Sink = fmt.Sprintf("dynamic property write %s[%s]", target, key)
			finding.Reachability = "confirmed"
			finding.Patchable = fn.name != ""
			finding.Status = FindingConfirmed
			normalized, err := NormalizeFinding(taskID, finding)
			if err == nil {
				findings = append(findings, normalized)
			}
		}
	}
	if len(findings) == 0 {
		for lineIndex, line := range lines {
			assignment := dynamicAssignmentPattern.FindStringSubmatch(line)
			if assignment == nil || guardedLines[lineIndex] {
				continue
			}
			findings = append(findings, uncertainDynamicFinding(taskID, name, lines, lineIndex, assignment[2], "unresolved source"))
			break
		}
	}
	return findings
}

func findJSFunctions(content string, lines []string) []jsFunction {
	matches := functionPattern.FindAllStringSubmatchIndex(content, -1)
	var out []jsFunction
	for _, match := range matches {
		name := ""
		if match[2] >= 0 {
			name = content[match[2]:match[3]]
		} else if match[6] >= 0 {
			name = content[match[6]:match[7]]
		}
		paramsText := ""
		if match[4] >= 0 {
			paramsText = content[match[4]:match[5]]
		} else if match[8] >= 0 {
			paramsText = content[match[8]:match[9]]
		}
		var params []string
		for _, param := range strings.Split(paramsText, ",") {
			params = append(params, strings.TrimSpace(param))
		}
		start := strings.Count(content[:match[0]], "\n")
		open := strings.Index(content[match[1]:], "{")
		if open < 0 {
			continue
		}
		open += match[1]
		endOffset := matchingBrace(content, open)
		if endOffset < 0 {
			continue
		}
		end := strings.Count(content[:endOffset], "\n") + 1
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, jsFunction{name: name, params: params, start: start, end: end})
	}
	return out
}

func matchingBrace(content string, open int) int {
	depth, quote := 0, rune(0)
	escaped := false
	for i, r := range content[open:] {
		if quote != 0 {
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote = r
			continue
		}
		if r == '{' {
			depth++
		}
		if r == '}' {
			depth--
			if depth == 0 {
				return open + i
			}
		}
	}
	return -1
}

func uncertainDynamicFinding(taskID, name string, lines []string, line int, key, source string) DefenseFinding {
	line = max(0, min(line, len(lines)-1))
	e := Evidence{Kind: "static_observation", Path: name, StartLine: line + 1, EndLine: line + 1, Snippet: lines[line], Description: "A dynamic property write needs manual data-flow review; no exploitability is assumed."}
	f := newFinding(taskID, "artex-static", StaticScannerVersion, RuleDynamicSet, "Dynamic property assignment requires review", "medium", "low", e)
	f.Source, f.Sink = source, "dynamic property assignment using "+key
	return f
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

type ConfigurationRule struct {
	ID       string   `json:"id"`
	Pattern  string   `json:"pattern"`
	Paths    []string `json:"paths"`
	Title    string   `json:"title"`
	Severity string   `json:"severity"`
}

func DefaultConfigurationRules() []ConfigurationRule {
	return []ConfigurationRule{
		{ID: "WORKFLOW_PULL_REQUEST_TARGET_HEAD", Pattern: `pull_request_target\s*:`, Paths: []string{".github/workflows/**"}, Title: "Privileged pull-request workflow requires review", Severity: "high"},
		{ID: "DEBUG_MODE_ENABLED", Pattern: `(?i)(?:debug|unsafe_mode)\s*[:=]\s*true\b`, Paths: []string{"**/*.json", "**/*.yaml", "**/*.yml", "**/*.toml"}, Title: "Debug or unsafe mode is enabled", Severity: "medium"},
	}
}

func ScanConfiguration(repo *Repository, taskID string, rules []ConfigurationRule) ([]DefenseFinding, ScanRecord, error) {
	record := ScanRecord{Scanner: "artex-config", Version: StaticScannerVersion, Command: []string{"scan.configuration", "explicit-rules"}}
	if len(rules) == 0 {
		rules = DefaultConfigurationRules()
	}
	var findings []DefenseFinding
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, record, fmt.Errorf("configuration rule %s: %w", rule.ID, err)
		}
		matches, err := repo.Search(rule.Pattern, rule.Paths)
		if err != nil {
			return nil, record, err
		}
		for _, match := range matches {
			if !re.MatchString(match.Content) {
				continue
			}
			e := Evidence{Kind: "configuration", Path: match.Path, StartLine: match.Line, EndLine: match.Line, Snippet: match.Content, Description: "The configuration matches an explicit review rule; deployment context is not inferred."}
			f := newFinding(taskID, "artex-config", StaticScannerVersion, RuleConfig+"_"+rule.ID, rule.Title, rule.Severity, "medium", e)
			f.Source, f.Sink = match.Path, rule.ID
			findings = append(findings, f)
		}
	}
	record.FindingCount = len(findings)
	return DeduplicateFindings(findings), record, nil
}

type secretRule struct {
	name       string
	expression *regexp.Regexp
}

var scanSecretRules = []secretRule{
	{"private-key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)},
	{"assigned-secret", regexp.MustCompile(`(?i)\b(?:api[_-]?key|client[_-]?secret|password|access[_-]?token)\s*[:=]\s*["'][^"']{8,}["']`)},
}

func ScanSecrets(repo *Repository, taskID string) ([]DefenseFinding, ScanRecord, error) {
	record := ScanRecord{Scanner: "artex-secrets", Version: StaticScannerVersion, Command: []string{"scan.secrets", "redacted-local-rules"}}
	files, err := repo.ListFiles(nil, []string{"**/*.png", "**/*.jpg", "**/*.zip", "**/vendor/**", "**/testdata/**"})
	if err != nil {
		return nil, record, err
	}
	var findings []DefenseFinding
	for _, file := range files {
		if file.Size > repo.Limits.MaxFileBytes {
			continue
		}
		content, err := repo.Read(file.Path, 0, 0)
		if err != nil {
			continue
		}
		for lineIndex, line := range strings.Split(content.Content, "\n") {
			for _, rule := range scanSecretRules {
				if !rule.expression.MatchString(line) {
					continue
				}
				e := Evidence{Kind: "redacted_secret", Path: file.Path, StartLine: lineIndex + 1, EndLine: lineIndex + 1, Snippet: redactEvidence(line), Description: "A likely secret was detected and redacted. Its value was not retained."}
				f := newFinding(taskID, "artex-secrets", StaticScannerVersion, RuleSecret+"_"+strings.ToUpper(strings.ReplaceAll(rule.name, "-", "_")), "Likely committed secret", "high", "medium", e)
				f.Source, f.Sink = file.Path, "repository content"
				findings = append(findings, f)
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Fingerprint < findings[j].Fingerprint })
	record.FindingCount = len(findings)
	return DeduplicateFindings(findings), record, nil
}

func sourceFileForFinding(f DefenseFinding) (string, error) {
	if len(f.Evidence) == 0 || f.Evidence[0].Path == "" {
		return "", fmt.Errorf("finding has no source file evidence")
	}
	return filepath.ToSlash(f.Evidence[0].Path), nil
}
