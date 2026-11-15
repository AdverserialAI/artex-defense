package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"artex/defense"
)

type defenseAPIExecutor struct{}

func (defenseAPIExecutor) EnvironmentDigest(context.Context) (string, error) {
	return "sha256:test-image", nil
}
func (defenseAPIExecutor) Execute(_ context.Context, _ string, command defense.Command, _ defense.ResourceLimits, _ bool) defense.CommandResult {
	code, output := 0, "actual test command passed"
	if command.Baseline {
		code, output = 1, "regression failed on vulnerable baseline"
	}
	return defense.CommandResult{Command: append([]string{command.Name}, command.Args...), Kind: command.Kind, Environment: "test-container", ExitCode: code, Output: output, StartedAt: time.Now().UTC(), Duration: time.Millisecond}
}

func defenseAPIServer(t *testing.T) *Server {
	t.Helper()
	return &Server{dataDir: t.TempDir(), defenseSessions: map[string]defenseSession{}, defenseExecutor: defenseAPIExecutor{}, jwtKey: bytes.Repeat([]byte{7}, 32)}
}

func defenseRequest(t *testing.T, handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var input bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&input).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &input)
	if _, rest, found := strings.Cut(target, "/api/defense/tasks/"); found {
		id, _, _ := strings.Cut(rest, "/")
		id, _, _ = strings.Cut(id, "?")
		req.SetPathValue("id", id)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, req)
	return recorder
}

func TestDefenseAPIAuditRunExportAndHumanApproval(t *testing.T) {
	s := defenseAPIServer(t)
	root, err := filepath.Abs("../defense/testdata/prototype-pollution")
	if err != nil {
		t.Fatal(err)
	}
	task := defense.DefaultTask(root)
	task.ID, task.AllowedWritePaths = "api-defense", []string{"merge.js", "merge.regression.test.js"}
	response := defenseRequest(t, s.defenseCreate, "POST", "/api/defense/tasks", map[string]any{"task": task, "mode": "run"})
	if response.Code != 200 {
		t.Fatalf("run status %d: %s", response.Code, response.Body.String())
	}
	var bundle defense.Bundle
	if err := json.Unmarshal(response.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Validation == nil || bundle.Validation.Status != defense.ValidationPassed {
		t.Fatalf("bundle %+v", bundle)
	}

	response = defenseRequest(t, s.defenseApprove, "POST", "/api/defense/tasks/api-defense/approve", map[string]bool{"confirm": false})
	if response.Code != 400 {
		t.Fatalf("unconfirmed approval status %d", response.Code)
	}
	response = defenseRequest(t, s.defenseApprove, "POST", "/api/defense/tasks/api-defense/approve", map[string]bool{"confirm": true})
	if response.Code != 200 {
		t.Fatalf("approval status %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Patch.Status != defense.PatchAccepted || bundle.Patch.ReviewedBy == defense.GeneratedBy {
		t.Fatalf("patch %+v", bundle.Patch)
	}

	req := httptest.NewRequest("GET", "/api/defense/tasks/api-defense/export?file=patch.diff", nil)
	req.SetPathValue("id", "api-defense")
	recorder := httptest.NewRecorder()
	s.defenseExport(recorder, req)
	if recorder.Code != 200 || !bytes.Contains(recorder.Body.Bytes(), []byte("__proto__")) {
		t.Fatalf("export %d %s", recorder.Code, recorder.Body.String())
	}
	response = defenseRequest(t, s.defenseExport, "GET", "/api/defense/tasks/api-defense/export?file=../task.json", nil)
	if response.Code != 400 {
		t.Fatalf("export traversal status %d", response.Code)
	}
}

func TestDefenseAPIRejectsInvalidTaskAndRequiresAuthentication(t *testing.T) {
	s := defenseAPIServer(t)
	response := defenseRequest(t, s.defenseCreate, "POST", "/api/defense/tasks", map[string]any{"task": defense.DefenseTask{}, "mode": "audit"})
	if response.Code != 400 {
		t.Fatalf("empty task status %d", response.Code)
	}

	req := httptest.NewRequest("GET", "/api/defense/tasks", nil)
	recorder := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.defenseList)).ServeHTTP(recorder, req)
	if recorder.Code != 401 {
		t.Fatalf("unauthenticated status %d", recorder.Code)
	}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/defense/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.defenseList)).ServeHTTP(recorder, req)
	if recorder.Code != 200 {
		t.Fatalf("authenticated status %d", recorder.Code)
	}
}
