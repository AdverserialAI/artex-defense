package defense

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type diffLine struct {
	kind                 byte
	text                 string
	oldNumber, newNumber int
}

func GenerateUnifiedDiff(changes []FileChange) (string, error) {
	var out strings.Builder
	for _, change := range changes {
		if !utf8.ValidString(change.OldContent) || !utf8.ValidString(change.NewContent) || strings.ContainsRune(change.OldContent+change.NewContent, 0) {
			return "", errors.New("binary patches are not supported")
		}
		if change.OldContent == change.NewContent {
			continue
		}
		oldPath, newPath := "a/"+change.Path, "b/"+change.Path
		if change.OldContent == "" {
			oldPath = "/dev/null"
		}
		if change.NewContent == "" {
			newPath = "/dev/null"
		}
		fmt.Fprintf(&out, "diff --git a/%s b/%s\n--- %s\n+++ %s\n", change.Path, change.Path, oldPath, newPath)
		lines := diffContentLines(change.OldContent, change.NewContent)
		if len(lines) == 0 {
			continue
		}
		first, last := -1, -1
		for i, line := range lines {
			if line.kind != ' ' {
				first = i
				break
			}
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if lines[i].kind != ' ' {
				last = i
				break
			}
		}
		start, end := max(0, first-3), min(len(lines), last+4)
		hunk := lines[start:end]
		oldStart, newStart := 0, 0
		oldCount, newCount := 0, 0
		for _, line := range hunk {
			if line.kind != '+' {
				if oldStart == 0 {
					oldStart = line.oldNumber
				}
				oldCount++
			}
			if line.kind != '-' {
				if newStart == 0 {
					newStart = line.newNumber
				}
				newCount++
			}
		}
		if oldCount == 0 {
			oldStart = max(0, hunk[0].oldNumber-1)
		}
		if newCount == 0 {
			newStart = max(0, hunk[0].newNumber-1)
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, line := range hunk {
			fmt.Fprintf(&out, "%c%s\n", line.kind, line.text)
		}
	}
	return out.String(), nil
}

func diffContentLines(oldContent, newContent string) []diffLine {
	oldLines, newLines := strings.Split(oldContent, "\n"), strings.Split(newContent, "\n")
	if oldContent == "" {
		oldLines = nil
	}
	if newContent == "" {
		newLines = nil
	}
	n, m := len(oldLines), len(newLines)
	if n*m > 4_000_000 {
		return simpleReplaceDiff(oldLines, newLines)
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffLine
	i, j, oldNo, newNo := 0, 0, 1, 1
	appendLine := func(kind byte, text string) {
		out = append(out, diffLine{kind: kind, text: text, oldNumber: oldNo, newNumber: newNo})
		if kind != '+' {
			oldNo++
		}
		if kind != '-' {
			newNo++
		}
	}
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			appendLine(' ', oldLines[i])
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			appendLine('-', oldLines[i])
			i++
		} else {
			appendLine('+', newLines[j])
			j++
		}
	}
	for i < n {
		appendLine('-', oldLines[i])
		i++
	}
	for j < m {
		appendLine('+', newLines[j])
		j++
	}
	return out
}

func simpleReplaceDiff(oldLines, newLines []string) []diffLine {
	out := make([]diffLine, 0, len(oldLines)+len(newLines))
	for i, line := range oldLines {
		out = append(out, diffLine{kind: '-', text: line, oldNumber: i + 1, newNumber: 1})
	}
	for i, line := range newLines {
		out = append(out, diffLine{kind: '+', text: line, oldNumber: len(oldLines) + 1, newNumber: i + 1})
	}
	return out
}

func ValidatePatch(task DefenseTask, patch *PatchProposal) error {
	if patch == nil || len(patch.Changes) == 0 {
		return errors.New("patch has no changes")
	}
	if len(patch.Changes) > task.Limits.MaxFilesChanged {
		return fmt.Errorf("patch changes %d files; limit is %d", len(patch.Changes), task.Limits.MaxFilesChanged)
	}
	changedLines, regression := 0, false
	seen := map[string]bool{}
	for _, change := range patch.Changes {
		rel := pathClean(change.Path)
		if rel != change.Path || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return fmt.Errorf("unsafe patch path %q", change.Path)
		}
		if seen[rel] {
			return fmt.Errorf("duplicate patch path %q", rel)
		}
		seen[rel] = true
		if !pathAllowed(rel, task.AllowedWritePaths) {
			return fmt.Errorf("patch path %q is outside allowed write scope", rel)
		}
		base := filepath.Base(rel)
		lower := strings.ToLower(base)
		if strings.Contains(lower, ".min.") || strings.HasSuffix(lower, ".map") || strings.Contains(rel, "/generated/") || strings.HasPrefix(rel, "generated/") {
			return fmt.Errorf("generated file %q cannot be patched", rel)
		}
		if !utf8.ValidString(change.OldContent+change.NewContent) || strings.ContainsRune(change.OldContent+change.NewContent, 0) {
			return errors.New("binary files cannot be patched")
		}
		if change.RegressionTest {
			regression = true
		}
		for _, line := range diffContentLines(change.OldContent, change.NewContent) {
			if line.kind != ' ' {
				changedLines++
			}
		}
	}
	if changedLines > task.Limits.MaxChangedLines {
		return fmt.Errorf("patch changes %d lines; limit is %d", changedLines, task.Limits.MaxChangedLines)
	}
	if !regression {
		return errors.New("patch must include a regression test")
	}
	if len(seen) == 1 && (strings.HasSuffix(patch.Changes[0].Path, "package-lock.json") || strings.HasSuffix(patch.Changes[0].Path, "go.sum")) {
		return errors.New("lockfile-only rewrites require a separately reviewed dependency remediation")
	}
	return nil
}

func ProposePatch(repo *Repository, task DefenseTask, finding DefenseFinding) (*PatchProposal, error) {
	if finding.Status != FindingConfirmed || finding.Reachability != "confirmed" || finding.Confidence == "low" {
		return nil, errors.New("uncertain findings require review and do not receive an invented patch")
	}
	if !finding.Patchable || finding.RuleID != RulePrototype {
		return nil, fmt.Errorf("finding %s has no safe deterministic patch", finding.ID)
	}
	path, err := sourceFileForFinding(finding)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(path) != ".js" {
		return nil, errors.New("prototype-pollution patching currently supports CommonJS .js files")
	}
	read, err := repo.Read(path, 0, 0)
	if err != nil || read.Truncated {
		return nil, errors.New("complete source file is required")
	}
	original, _ := RedactSecrets(read.Content)
	if original != read.Content {
		return nil, errors.New("source contains a likely secret; manual review required")
	}
	loopLine, _ := strconv.Atoi(finding.Evidence[0].Data["loop_line"])
	key := finding.Evidence[0].Data["key_variable"]
	if loopLine <= 0 || key == "" {
		return nil, errors.New("finding lacks patch location evidence")
	}
	lines := strings.Split(original, "\n")
	if loopLine > len(lines) || !forInPattern.MatchString(lines[loopLine-1]) {
		return nil, errors.New("source no longer matches finding evidence")
	}
	indent := lines[loopLine-1][:len(lines[loopLine-1])-len(strings.TrimLeft(lines[loopLine-1], " \t"))]
	guard := indent + "  if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;"
	if key != "key" {
		guard = indent + fmt.Sprintf("  if (%s === '__proto__' || %s === 'constructor' || %s === 'prototype') continue;", key, key, key)
	}
	patchedLines := append([]string(nil), lines...)
	patchedLines = append(patchedLines[:loopLine], append([]string{guard}, patchedLines[loopLine:]...)...)
	patched := strings.Join(patchedLines, "\n")
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	testPath := filepath.ToSlash(filepath.Join(filepath.Dir(path), base+".regression.test.js"))
	testContent := regressionTest(path, finding.Evidence[0].Data["function"])
	proposal := &PatchProposal{
		SchemaVersion: SchemaVersion, ID: timestampID("patch"), FindingID: finding.ID,
		Summary:   "Reject prototype-mutating keys during recursive object merge",
		Rationale: "Block __proto__, constructor, and prototype before the dynamic write while preserving ordinary nested merge behavior.",
		RiskLevel: "low", Status: PatchDraft, GeneratedBy: GeneratedBy, CreatedAt: time.Now().UTC(),
		Changes:         []FileChange{{Path: path, OldContent: original, NewContent: patched}, {Path: testPath, OldContent: "", NewContent: testContent, RegressionTest: true}},
		RegressionTests: []string{testPath},
	}
	for _, change := range proposal.Changes {
		proposal.FilesChanged = append(proposal.FilesChanged, change.Path)
	}
	if err := ValidatePatch(task, proposal); err != nil {
		return nil, err
	}
	proposal.UnifiedDiff, err = GenerateUnifiedDiff(proposal.Changes)
	if err != nil || proposal.UnifiedDiff == "" {
		return nil, errors.New("could not generate a unified diff")
	}
	return proposal, nil
}

func regressionTest(sourcePath, functionName string) string {
	importPath := "./" + filepath.Base(sourcePath)
	return fmt.Sprintf(`'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const %s = require(%q);

test('merge preserves nested behavior without polluting prototypes', () => {
  const target = { safe: { original: true } };
  const input = JSON.parse('{"safe":{"added":true},"__proto__":{"polluted":"yes"},"constructor":{"prototype":{"polluted":"yes"}},"prototype":{"polluted":"yes"}}');

  %s(target, input);

  assert.equal({}.polluted, undefined);
  assert.equal(Object.prototype.polluted, undefined);
  assert.deepEqual(target.safe, { original: true, added: true });
  assert.equal(target.regular, undefined);
});

test('merge still copies ordinary nested properties', () => {
  const target = { nested: { left: 1 } };
  %s(target, { nested: { right: 2 }, regular: 'value' });
  assert.deepEqual(target, { nested: { left: 1, right: 2 }, regular: 'value' });
});
`, functionName, importPath, functionName, functionName)
}

func ApplyUnifiedDiff(root, diff string) error {
	changes, err := parseUnifiedDiff(root, diff)
	if err != nil {
		return err
	}
	for path, content := range changes {
		if pathClean(path) != path || path == "." || strings.HasPrefix(path, "../") {
			return errors.New("unsafe diff path")
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink patch target denied")
		}
		if content == nil {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(*content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type parsedDiffHunk struct {
	oldStart           int
	oldLines, newLines []string
}

type parsedDiffFile struct {
	oldPath, newPath string
	hunks            []parsedDiffHunk
}

func parseUnifiedDiff(root, diff string) (map[string]*string, error) {
	scanner := bufio.NewScanner(strings.NewReader(diff))
	var current *parsedDiffFile
	result := map[string]*string{}
	flush := func() error {
		if current == nil {
			return nil
		}
		if len(current.hunks) == 0 || current.newPath == "" {
			return errors.New("diff file has no valid hunk")
		}
		var original []byte
		var err error
		if current.oldPath != "/dev/null" {
			full := filepath.Join(root, filepath.FromSlash(current.oldPath))
			info, statErr := os.Lstat(full)
			if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("symlink diff source denied")
			}
			original, err = os.ReadFile(full)
			if err != nil {
				return err
			}
		}
		var originalLines []string
		if len(original) > 0 {
			originalLines = strings.Split(string(original), "\n")
		}
		var output []string
		oldIndex := 0
		for _, hunk := range current.hunks {
			start := hunk.oldStart - 1
			if len(hunk.oldLines) == 0 {
				start = hunk.oldStart
			}
			if start < oldIndex || start > len(originalLines) || start+len(hunk.oldLines) > len(originalLines) {
				return fmt.Errorf("diff hunk is outside %s", current.oldPath)
			}
			output = append(output, originalLines[oldIndex:start]...)
			if !equalLines(originalLines[start:start+len(hunk.oldLines)], hunk.oldLines) {
				return fmt.Errorf("diff does not apply cleanly to %s", current.oldPath)
			}
			output = append(output, hunk.newLines...)
			oldIndex = start + len(hunk.oldLines)
		}
		output = append(output, originalLines[oldIndex:]...)
		if current.newPath == "/dev/null" {
			if len(output) != 0 {
				return errors.New("deleted file has added content")
			}
			result[current.oldPath] = nil
		} else {
			content := strings.Join(output, "\n")
			result[current.newPath] = &content
		}
		current = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git ") {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			if err := flush(); err != nil {
				return nil, err
			}
			name := strings.TrimPrefix(line, "--- ")
			current = &parsedDiffFile{oldPath: strings.TrimPrefix(name, "a/")}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			if current == nil {
				return nil, errors.New("diff target has no source")
			}
			current.newPath = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			if current == nil || current.newPath == "" {
				return nil, errors.New("diff hunk has no target")
			}
			var oldStart int
			if _, err := fmt.Sscanf(line, "@@ -%d", &oldStart); err != nil {
				return nil, errors.New("invalid diff hunk header")
			}
			current.hunks = append(current.hunks, parsedDiffHunk{oldStart: oldStart})
			continue
		}
		if current == nil || len(current.hunks) == 0 {
			continue
		}
		if line == "" {
			return nil, errors.New("empty unprefixed diff line")
		}
		hunk := &current.hunks[len(current.hunks)-1]
		switch line[0] {
		case ' ':
			hunk.oldLines = append(hunk.oldLines, line[1:])
			hunk.newLines = append(hunk.newLines, line[1:])
		case '+':
			hunk.newLines = append(hunk.newLines, line[1:])
		case '-':
			hunk.oldLines = append(hunk.oldLines, line[1:])
		case '\\':
		default:
			return nil, fmt.Errorf("invalid diff line %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("empty or invalid unified diff")
	}
	return result, nil
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
