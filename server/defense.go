package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"artex/defense"
)

type defenseSession struct {
	Bundle    defense.Bundle
	UpdatedAt time.Time
}

func (s *Server) defenseSession(id string) (defenseSession, bool) {
	s.defenseMu.Lock()
	defer s.defenseMu.Unlock()
	session, ok := s.defenseSessions[id]
	return session, ok
}

func (s *Server) saveDefenseSession(session defenseSession) {
	s.defenseMu.Lock()
	defer s.defenseMu.Unlock()
	if s.defenseSessions == nil {
		s.defenseSessions = map[string]defenseSession{}
	}
	session.UpdatedAt = time.Now().UTC()
	s.defenseSessions[session.Bundle.Task.ID] = session
}

func (s *Server) defenseCreate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Task defense.DefenseTask `json:"task"`
		Mode string              `json:"mode"`
	}
	if err := decodeJSONBounded(w, r, &request, 1<<20); err != nil {
		if !writeJSONBodyTooLarge(w, err) {
			writeErr(w, 400, "invalid defense task: "+err.Error())
		}
		return
	}
	if request.Mode == "" {
		request.Mode = "audit"
	}
	if request.Mode != "audit" && request.Mode != "run" {
		writeErr(w, 400, "mode must be audit or run")
		return
	}
	if request.Task.CreatedAt.IsZero() {
		request.Task.CreatedAt = time.Now().UTC()
	}
	coordinator, err := defense.NewCoordinator(request.Task)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, exists := s.defenseSession(coordinator.Task.ID); exists {
		writeErr(w, 409, "defense task already exists")
		return
	}
	if s.defenseExecutor != nil {
		coordinator.Executor = s.defenseExecutor
	}
	bundle := defense.Bundle{Task: coordinator.Task}
	if request.Mode == "audit" {
		assessment, auditErr := coordinator.Audit(r.Context())
		bundle.Assessment = assessment
		if auditErr != nil {
			writeErr(w, 400, auditErr.Error())
			return
		}
		if len(assessment.Findings) > 0 {
			bundle.Task.Status = defense.TaskNeedsReview
		} else {
			bundle.Task.Status = defense.TaskCompleted
		}
	} else {
		var runErr error
		bundle, runErr = coordinator.Run(r.Context())
		session := defenseSession{Bundle: bundle}
		s.saveDefenseSession(session)
		if exportErr := defense.ExportBundle(bundle, filepath.Join(s.dataDir, "defense", bundle.Task.ID)); exportErr != nil {
			writeErr(w, 500, exportErr.Error())
			return
		}
		if runErr != nil {
			writeErr(w, 422, runErr.Error())
			return
		}
		writeJSON(w, 200, bundle)
		return
	}
	bundle.Report = defense.RenderRemediation(bundle)
	s.saveDefenseSession(defenseSession{Bundle: bundle})
	if err := defense.ExportBundle(bundle, filepath.Join(s.dataDir, "defense", bundle.Task.ID)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, bundle)
}

func (s *Server) defenseList(w http.ResponseWriter, r *http.Request) {
	s.defenseMu.Lock()
	items := make([]map[string]any, 0, len(s.defenseSessions))
	for _, session := range s.defenseSessions {
		items = append(items, map[string]any{"id": session.Bundle.Task.ID, "repository_root": session.Bundle.Task.RepositoryRoot, "status": session.Bundle.Task.Status, "created_at": session.Bundle.Task.CreatedAt, "updated_at": session.UpdatedAt, "finding_count": len(session.Bundle.Assessment.Findings), "patch_status": patchStatus(session.Bundle), "validation_status": validationStatus(session.Bundle)})
	}
	s.defenseMu.Unlock()
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["updated_at"]) > fmt.Sprint(items[j]["updated_at"]) })
	writeJSON(w, 200, items)
}

func patchStatus(bundle defense.Bundle) string {
	if bundle.Patch == nil {
		return ""
	}
	return string(bundle.Patch.Status)
}
func validationStatus(bundle defense.Bundle) string {
	if bundle.Validation == nil {
		return ""
	}
	return string(bundle.Validation.Status)
}

func (s *Server) defenseGet(w http.ResponseWriter, r *http.Request) {
	session, ok := s.defenseSession(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "defense task not found")
		return
	}
	writeJSON(w, 200, session.Bundle)
}

func (s *Server) defenseApprove(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSONBounded(w, r, &request, 4096); err != nil {
		writeErr(w, 400, "invalid review request")
		return
	}
	if !request.Confirm {
		writeErr(w, 400, "explicit human confirmation is required")
		return
	}
	session, ok := s.defenseSession(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "defense task not found")
		return
	}
	if err := defense.ReviewBundle(&session.Bundle, "ARTEX console reviewer"); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.saveDefenseSession(session)
	if err := defense.ExportBundle(session.Bundle, filepath.Join(s.dataDir, "defense", session.Bundle.Task.ID)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, session.Bundle)
}

func (s *Server) defenseExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, ok := s.defenseSession(id)
	if !ok {
		writeErr(w, 404, "defense task not found")
		return
	}
	name := r.URL.Query().Get("file")
	if name == "" {
		name = "bundle.json"
	}
	allowed := map[string]string{"bundle.json": "application/json", "patch.diff": "text/plain", "remediation.md": "text/markdown", "validation.json": "application/json", "findings.json": "application/json"}
	contentType, ok := allowed[name]
	if !ok {
		writeErr(w, 400, "unsupported export artifact")
		return
	}
	path := filepath.Join(s.dataDir, "defense", id, name)
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 404, "export artifact not found")
		return
	}
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func defenseTaskFromJSON(data []byte) (defense.DefenseTask, error) {
	var task defense.DefenseTask
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil {
		return task, err
	}
	return task, defense.ValidateTask(&task)
}
