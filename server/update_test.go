package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// No manager, database, or provider is needed: update endpoints are strictly local.
func TestLegacyUpdateEndpointsStayDisabled(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name, method, path string
		handler            http.HandlerFunc
		status             int
	}{
		{"check", "GET", "/api/update/check?force=true", s.updateCheck, http.StatusOK},
		{"apply", "POST", "/api/update/apply", s.updateApply, http.StatusForbidden},
		{"rollback", "POST", "/api/update/rollback", s.updateRollback, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"version":"v99.0.0"}`)))
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d", w.Code, tc.status)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.name == "check" && body["disabled"] != true {
				t.Fatalf("check must remain disabled: %v", body)
			}
			if !strings.Contains(w.Body.String(), "redeploying") {
				t.Fatalf("missing redeployment policy: %s", w.Body.String())
			}
		})
	}
}

func TestLegacyUpdateStreamTerminatesDisabled(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).updateStream(w, httptest.NewRequest("GET", "/api/update/stream", nil))
	if w.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(w.Body.String(), `"disabled":true`) {
		t.Fatalf("unexpected stream: %s", w.Body.String())
	}
}
