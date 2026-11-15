package defense

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var allowedExecutables = map[string]bool{
	"go": true, "node": true, "npm": true, "govulncheck": true,
}

func DefaultTask(root string) DefenseTask {
	return DefenseTask{
		SchemaVersion:    SchemaVersion,
		Profile:          Profile,
		RepositoryRoot:   root,
		DefaultBranch:    "main",
		AllowedReadPaths: []string{"."},
		Limits: ResourceLimits{
			MaxFileBytes: 1 << 20, MaxContentBytes: 128 << 10, MaxSearchResults: 200,
			MaxFilesChanged: 5, MaxChangedLines: 250, TimeoutSeconds: 120,
			MemoryMB: 1024, CPU: 1000, PIDs: 128, OutputBytes: 64 << 10,
		},
		Validation: ValidationPolicy{
			Image: "node:22-bookworm", RequireBaselineFailure: true,
			CommandAllowlist: []string{"go", "node", "npm", "govulncheck"},
		},
		Status:    TaskDraft,
		CreatedAt: time.Now().UTC(),
	}
}

func ValidateTask(task *DefenseTask) error {
	if task == nil {
		return errors.New("defense task is required")
	}
	if task.SchemaVersion == "" {
		task.SchemaVersion = SchemaVersion
	}
	if task.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported task schema %q", task.SchemaVersion)
	}
	if task.Profile != Profile {
		return fmt.Errorf("execution profile must be %q", Profile)
	}
	if strings.TrimSpace(task.RepositoryRoot) == "" {
		return errors.New("repository_root is required")
	}
	abs, err := filepath.Abs(task.RepositoryRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repository root is not a readable directory: %s", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	task.RepositoryRoot = filepath.Clean(resolved)
	if task.ID == "" {
		task.ID = stableID("task", task.RepositoryRoot, task.CreatedAt.UTC().Format(time.RFC3339Nano))
	} else if !validTaskID(task.ID) {
		return errors.New("task id must be 1-128 characters using letters, digits, dot, underscore, or dash")
	}
	if len(task.AllowedReadPaths) == 0 {
		return errors.New("at least one allowed read path is required")
	}
	if task.AllowedReadPaths, err = cleanScopePaths(task.AllowedReadPaths); err != nil {
		return fmt.Errorf("allowed read paths: %w", err)
	}
	if task.AllowedWritePaths, err = cleanScopePaths(task.AllowedWritePaths); err != nil {
		return fmt.Errorf("allowed write paths: %w", err)
	}
	defaults := DefaultTask("").Limits
	if task.Limits.MaxFileBytes <= 0 {
		task.Limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if task.Limits.MaxContentBytes <= 0 {
		task.Limits.MaxContentBytes = defaults.MaxContentBytes
	}
	if task.Limits.MaxSearchResults <= 0 {
		task.Limits.MaxSearchResults = defaults.MaxSearchResults
	}
	if task.Limits.MaxFilesChanged <= 0 {
		task.Limits.MaxFilesChanged = defaults.MaxFilesChanged
	}
	if task.Limits.MaxChangedLines <= 0 {
		task.Limits.MaxChangedLines = defaults.MaxChangedLines
	}
	if task.Limits.TimeoutSeconds <= 0 {
		task.Limits.TimeoutSeconds = defaults.TimeoutSeconds
	}
	if task.Limits.MemoryMB <= 0 {
		task.Limits.MemoryMB = defaults.MemoryMB
	}
	if task.Limits.CPU <= 0 {
		task.Limits.CPU = defaults.CPU
	}
	if task.Limits.PIDs <= 0 {
		task.Limits.PIDs = defaults.PIDs
	}
	if task.Limits.OutputBytes <= 0 {
		task.Limits.OutputBytes = defaults.OutputBytes
	}
	if task.Limits.MaxFileBytes > 10<<20 || task.Limits.MaxContentBytes > 1<<20 || task.Limits.MaxSearchResults > 5000 || task.Limits.MaxFilesChanged > 20 || task.Limits.MaxChangedLines > 2000 {
		return errors.New("repository or patch limits exceed defense maxima")
	}
	if task.Limits.TimeoutSeconds > 900 || task.Limits.MemoryMB > 8192 || task.Limits.CPU > 4000 || task.Limits.PIDs > 512 || task.Limits.OutputBytes > 1<<20 {
		return errors.New("validation resource limits exceed defense maxima")
	}
	if task.Validation.Image == "" {
		return errors.New("a validation container image is required")
	}
	if len(task.Validation.CommandAllowlist) == 0 {
		task.Validation.CommandAllowlist = append([]string(nil), defaultsCommandAllowlist()...)
	}
	for _, name := range task.Validation.CommandAllowlist {
		if !allowedExecutables[name] {
			return fmt.Errorf("executable %q is not supported by the defense profile", name)
		}
	}
	if len(task.Validation.ScannerCommand) > 0 {
		if err := ValidateCommand(task.Validation.ScannerCommand, task.Validation.CommandAllowlist); err != nil {
			return fmt.Errorf("scanner command: %w", err)
		}
	}
	if task.Status == "" {
		task.Status = TaskDraft
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	return nil
}

func validTaskID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		alphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func defaultsCommandAllowlist() []string {
	out := make([]string, 0, len(allowedExecutables))
	for name := range allowedExecutables {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func cleanScopePaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, "\x00") {
			return nil, fmt.Errorf("absolute or invalid path %q", p)
		}
		p = strings.TrimPrefix(pathClean(p), "./")
		if p == ".." || strings.HasPrefix(p, "../") {
			return nil, fmt.Errorf("path escapes repository: %q", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func pathClean(p string) string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) == 0 {
				return "../" + strings.Join(parts, "/")
			}
			out = out[:len(out)-1]
		default:
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "."
	}
	return strings.Join(out, "/")
}

func pathAllowed(rel string, allowed []string) bool {
	rel = pathClean(filepath.ToSlash(rel))
	for _, base := range allowed {
		base = pathClean(base)
		if base == "." || rel == base || strings.HasPrefix(rel, base+"/") {
			return true
		}
	}
	return false
}

func ValidateCommand(argv, allowlist []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("empty command")
	}
	name := filepath.Base(argv[0])
	if name != argv[0] || !allowedExecutables[name] {
		return fmt.Errorf("command %q denied: direct allowlisted executable required", argv[0])
	}
	allowed := false
	for _, item := range allowlist {
		if item == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("command %q is not allowlisted for this task", name)
	}
	for _, arg := range argv[1:] {
		if strings.ContainsRune(arg, '\x00') || strings.ContainsAny(arg, "\r\n") {
			return errors.New("command arguments contain control characters")
		}
	}
	return nil
}

func stableID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}
