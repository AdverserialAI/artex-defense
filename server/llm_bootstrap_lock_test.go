package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeasuredRuntimeBootstrapBlocksCredentialPersistence(t *testing.T) {
	t.Setenv("ARTEX_LLM_BOOTSTRAP_FILE", "/run/adverserial/current/artex-llm.json")
	s := &Server{}
	for name, handler := range map[string]http.HandlerFunc{
		"legacy llm":   s.setLLM,
		"profile save": s.pgSaveProfile,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"api_key":"must-not-persist"}`))
			response := httptest.NewRecorder()
			handler(response, request)
			if response.Code != http.StatusLocked {
				t.Fatalf("status=%d, want %d", response.Code, http.StatusLocked)
			}
		})
	}
	profileID := int64(42)
	if got := s.effectiveProfileForAgent("planner", &profileID); got != nil {
		t.Fatalf("managed runtime must ignore profile pins: %v", *got)
	}
	if _, ok := s.loadProfileConfig(profileID); ok {
		t.Fatal("managed runtime must not load database provider credentials")
	}
}
