package defense

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Repository struct {
	Root      string
	Limits    ResourceLimits
	ReadPaths []string
}

type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type ReadResult struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Redacted  bool   `json:"redacted"`
}

type SearchMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type Manifest struct {
	Path         string            `json:"path"`
	Ecosystem    string            `json:"ecosystem"`
	Name         string            `json:"name,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Scripts      map[string]string `json:"scripts,omitempty"`
}

type TestLayout struct {
	Ecosystem string   `json:"ecosystem"`
	Files     []string `json:"files"`
}

func NewRepository(task DefenseTask) (*Repository, error) {
	if err := ValidateTask(&task); err != nil {
		return nil, err
	}
	return &Repository{Root: task.RepositoryRoot, Limits: task.Limits, ReadPaths: task.AllowedReadPaths}, nil
}

func (r *Repository) resolve(name string, allowRoot bool) (string, string, error) {
	if name == "" {
		name = "."
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, '\x00') {
		return "", "", errors.New("absolute and NUL-containing paths are denied")
	}
	rel := pathClean(filepath.ToSlash(name))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", errors.New("path escapes repository root")
	}
	if rel == "." && !allowRoot {
		return "", "", errors.New("repository root is not a file")
	}
	if !pathAllowed(rel, r.ReadPaths) {
		return "", "", fmt.Errorf("path %q is outside allowed read scope", rel)
	}
	full := filepath.Join(r.Root, filepath.FromSlash(rel))
	current := r.Root
	parts := strings.Split(rel, "/")
	if rel == "." {
		parts = nil
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("symlink denied: %s", rel)
		}
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", "", err
	}
	within, err := filepath.Rel(r.Root, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", "", errors.New("resolved path escapes repository root")
	}
	return full, rel, nil
}

func (r *Repository) Read(name string, startLine, endLine int) (ReadResult, error) {
	full, rel, err := r.resolve(name, false)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ReadResult{}, errors.New("only regular files can be read")
	}
	if info.Size() > r.Limits.MaxFileBytes {
		return ReadResult{}, fmt.Errorf("file exceeds %d-byte limit", r.Limits.MaxFileBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ReadResult{}, err
	}
	if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
		return ReadResult{}, errors.New("binary or non-UTF-8 files are not readable")
	}
	if startLine <= 0 {
		startLine = 1
	}
	lines := strings.Split(string(data), "\n")
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) || endLine < startLine {
		return ReadResult{}, errors.New("invalid line range")
	}
	content := strings.Join(lines[startLine-1:endLine], "\n")
	truncated := false
	if len(content) > r.Limits.MaxContentBytes {
		content = content[:r.Limits.MaxContentBytes]
		for !utf8.ValidString(content) {
			content = content[:len(content)-1]
		}
		truncated = true
	}
	redacted, changed := RedactSecrets(content)
	return ReadResult{Path: rel, StartLine: startLine, EndLine: endLine, Content: redacted, Truncated: truncated, Redacted: changed}, nil
}

func (r *Repository) ListFiles(include, exclude []string) ([]FileEntry, error) {
	var out []FileEntry
	err := filepath.WalkDir(r.Root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(r.Root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if rel == ".git" || rel == "node_modules" || rel == "dist" || rel == "out" || rel == ".next" {
				return filepath.SkipDir
			}
			return nil
		}
		included := len(include) == 0 || matchesAny(rel, include)
		if entry.Type()&os.ModeSymlink != 0 {
			if included && !matchesAny(rel, exclude) && pathAllowed(rel, r.ReadPaths) {
				return fmt.Errorf("symlink denied: %s", rel)
			}
			return nil
		}
		if !entry.Type().IsRegular() || !pathAllowed(rel, r.ReadPaths) || !included || matchesAny(rel, exclude) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, FileEntry{Path: rel, Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func matchesAny(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if globMatch(pattern, name) {
			return true
		}
	}
	return false
}

func globMatch(pattern, name string) bool {
	pattern = pathClean(filepath.ToSlash(pattern))
	name = pathClean(filepath.ToSlash(name))
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteByte('$')
	matched, err := regexp.MatchString(b.String(), name)
	return err == nil && matched
}

func (r *Repository) Search(query string, paths []string) ([]SearchMatch, error) {
	re, err := regexp.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("invalid search expression: %w", err)
	}
	if len(paths) == 0 {
		paths = r.ReadPaths
	}
	var includes []string
	for _, p := range paths {
		p = pathClean(p)
		if p == "." {
			includes = append(includes, "**")
		} else {
			includes = append(includes, p, p+"/**")
		}
	}
	files, err := r.ListFiles(includes, nil)
	if err != nil {
		return nil, err
	}
	var out []SearchMatch
	for _, file := range files {
		if file.Size > r.Limits.MaxFileBytes {
			continue
		}
		result, err := r.Read(file.Path, 0, 0)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(result.Content))
		line := 0
		for scanner.Scan() {
			line++
			if re.MatchString(scanner.Text()) {
				out = append(out, SearchMatch{Path: file.Path, Line: line, Content: scanner.Text()})
				if len(out) >= r.Limits.MaxSearchResults {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func (r *Repository) Manifest(name string) (Manifest, error) {
	_, rel, err := r.resolve(name, false)
	if err != nil {
		return Manifest{}, err
	}
	result, err := r.Read(rel, 0, 0)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Path: rel, Dependencies: map[string]string{}}
	switch filepath.Base(rel) {
	case "package.json":
		var p struct {
			Name                          string `json:"name"`
			Dependencies, DevDependencies map[string]string
			Scripts                       map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal([]byte(result.Content), &p); err != nil {
			return m, err
		}
		m.Ecosystem, m.Name, m.Scripts = "npm", p.Name, p.Scripts
		for k, v := range p.Dependencies {
			m.Dependencies[k] = v
		}
		for k, v := range p.DevDependencies {
			m.Dependencies[k] = v
		}
	case "go.mod":
		m.Ecosystem = "go"
		for _, line := range strings.Split(result.Content, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "module" {
				m.Name = fields[1]
			}
			if len(fields) >= 2 && fields[0] != "module" && fields[0] != "go" && fields[0] != "require" && strings.Contains(fields[0], ".") && strings.HasPrefix(fields[1], "v") {
				m.Dependencies[fields[0]] = fields[1]
			}
			if len(fields) == 3 && fields[0] == "require" {
				m.Dependencies[fields[1]] = fields[2]
			}
		}
	default:
		return m, errors.New("supported manifests are package.json and go.mod")
	}
	return m, nil
}

func (r *Repository) TestLayout() ([]TestLayout, error) {
	files, err := r.ListFiles(nil, nil)
	if err != nil {
		return nil, err
	}
	layouts := map[string]*TestLayout{}
	for _, file := range files {
		ecosystem := ""
		switch {
		case strings.HasSuffix(file.Path, "_test.go"):
			ecosystem = "go"
		case strings.HasSuffix(file.Path, ".test.js"), strings.HasSuffix(file.Path, ".spec.js"), strings.HasSuffix(file.Path, ".test.cjs"):
			ecosystem = "npm"
		}
		if ecosystem != "" {
			if layouts[ecosystem] == nil {
				layouts[ecosystem] = &TestLayout{Ecosystem: ecosystem}
			}
			layouts[ecosystem].Files = append(layouts[ecosystem].Files, file.Path)
		}
	}
	out := make([]TestLayout, 0, len(layouts))
	for _, layout := range layouts {
		out = append(out, *layout)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ecosystem < out[j].Ecosystem })
	return out, nil
}
