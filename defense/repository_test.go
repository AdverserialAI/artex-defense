package defense

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRepository(t *testing.T) (*Repository, DefenseTask) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"fixture","scripts":{"test":"node --test"},"dependencies":{"x":"1.0.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.js"), []byte("const value = 1;\nconst password = 'supersecret';\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := DefaultTask(root)
	task.ID, task.CreatedAt = "task-test", time.Unix(1, 0).UTC()
	task.AllowedWritePaths = []string{"index.js", "index.regression.test.js"}
	if err := ValidateTask(&task); err != nil {
		t.Fatal(err)
	}
	repo, err := NewRepository(task)
	if err != nil {
		t.Fatal(err)
	}
	return repo, task
}

func TestValidateTaskRejectsEmptyAndUnsafePolicy(t *testing.T) {
	if err := ValidateTask(&DefenseTask{}); err == nil {
		t.Fatal("empty task accepted")
	}
	_, task := testRepository(t)
	task.ID = "../escape"
	if err := ValidateTask(&task); err == nil {
		t.Fatal("unsafe task id accepted")
	}
	task.ID = "task-test"
	task.Limits.TimeoutSeconds = 100000
	if err := ValidateTask(&task); err == nil {
		t.Fatal("excessive timeout accepted")
	}
}

func TestRepositoryRejectsTraversalAndSymlinkEscape(t *testing.T) {
	repo, _ := testRepository(t)
	for _, path := range []string{"../outside", "/etc/passwd", "a/../../outside"} {
		if _, err := repo.Read(path, 0, 0); err == nil {
			t.Fatalf("read %q succeeded", path)
		}
	}
	outside := filepath.Join(filepath.Dir(repo.Root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("do not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo.Root, "escape.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read("escape.js", 0, 0); err == nil {
		t.Fatal("symlink escape succeeded")
	}
	if _, err := repo.ListFiles([]string{"**/escape.js"}, nil); err == nil {
		t.Fatal("listing included a symlink")
	}
}

func TestRepositoryReadRedactsAndCapsContent(t *testing.T) {
	repo, task := testRepository(t)
	result, err := repo.Read("index.js", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Redacted || strings.Contains(result.Content, "supersecret") {
		t.Fatalf("secret was not redacted: %q", result.Content)
	}
	task.Limits.MaxContentBytes = 8
	repo.Limits = task.Limits
	result, err = repo.Read("index.js", 0, 0)
	if err != nil || !result.Truncated || len(result.Content) > 8 {
		t.Fatalf("content cap not enforced: %+v %v", result, err)
	}
}

func TestSecretRedactionForms(t *testing.T) {
	input := `Authorization: Bearer abc.def.ghi
api_key = "1234567890abcdef"
https://user:password@example.com
AKIA1234567890ABCDEF`
	output, changed := RedactSecrets(input)
	if !changed {
		t.Fatal("secrets not detected")
	}
	for _, secret := range []string{"abc.def.ghi", "1234567890abcdef", "password@", "AKIA1234567890ABCDEF"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q remains in %q", secret, output)
		}
	}
}

func TestRepositoryManifestSearchAndTestLayout(t *testing.T) {
	repo, _ := testRepository(t)
	if err := os.WriteFile(filepath.Join(repo.Root, "index.test.js"), []byte("test('x', () => {});"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := repo.Manifest("package.json")
	if err != nil || manifest.Ecosystem != "npm" || manifest.Dependencies["x"] != "1.0.0" {
		t.Fatalf("manifest: %+v %v", manifest, err)
	}
	matches, err := repo.Search(`const\s+value`, nil)
	if err != nil || len(matches) != 1 || matches[0].Path != "index.js" {
		t.Fatalf("search: %+v %v", matches, err)
	}
	layout, err := repo.TestLayout()
	if err != nil || len(layout) != 1 || len(layout[0].Files) != 1 {
		t.Fatalf("layout: %+v %v", layout, err)
	}
}

func TestCommandPolicyDeniesShellAndControlsTimeoutValues(t *testing.T) {
	for _, argv := range [][]string{{"sh", "-c", "id"}, {"bash"}, {"/usr/bin/node", "--test"}, {"node", "ok\nbad"}} {
		if err := ValidateCommand(argv, []string{"node"}); err == nil {
			t.Fatalf("command accepted: %v", argv)
		}
	}
	if err := ValidateCommand([]string{"node", "--test"}, []string{"node"}); err != nil {
		t.Fatal(err)
	}
}
