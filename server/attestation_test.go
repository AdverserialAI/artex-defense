package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fakeDstack(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("DSTACK_SOCKET", socket)
}

func TestAttestationQuoteRejectsInvalidReportData(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/attestation?reportData=not-hex", nil)
	response := httptest.NewRecorder()
	(&Server{}).attestationQuote(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", response.Code)
	}
}

func TestAttestationQuoteReturnsGuestEvidence(t *testing.T) {
	fakeDstack(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/Info" {
			_, _ = w.Write([]byte(`{"cloud_vendor":""}`))
			return
		}
		if r.URL.Path != "/GetQuote" {
			t.Errorf("unexpected guest path %q", r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["report_data"] != "abcd" {
			t.Errorf("unexpected report_data %q", payload["report_data"])
		}
		_, _ = w.Write([]byte(`{"quote":"deadbeef","event_log":"[]","report_data":"abcd"}`))
	})
	request := httptest.NewRequest(http.MethodGet, "/attestation?reportData=abcd", nil)
	response := httptest.NewRecorder()
	(&Server{}).attestationQuote(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", response.Code, response.Body.String())
	}
	var evidence QuoteEvidenceForTest
	if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Quote != "deadbeef" || evidence.ReportData != "abcd" {
		t.Fatalf("unexpected evidence %+v", evidence)
	}
}

func TestAttestationQuoteIncludesFullGCPAttestation(t *testing.T) {
	fakeDstack(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Info":
			_, _ = w.Write([]byte(`{"cloud_vendor":"Google"}`))
		case "/GetQuote":
			_, _ = w.Write([]byte(`{"quote":"deadbeef","event_log":"[]","report_data":"abcd"}`))
		case "/Attest":
			_, _ = w.Write([]byte(`{"attestation":"cafebabe"}`))
		default:
			t.Errorf("unexpected guest path %q", r.URL.Path)
		}
	})
	request := httptest.NewRequest(http.MethodGet, "/attestation?reportData=abcd", nil)
	response := httptest.NewRecorder()
	(&Server{}).attestationQuote(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", response.Code, response.Body.String())
	}
	var evidence QuoteEvidenceForTest
	if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Attestation != "cafebabe" {
		t.Fatalf("expected full GCP attestation, got %+v", evidence)
	}
}

func TestAttestationQuoteFailsClosedWhenRuntimeInfoIsUnavailable(t *testing.T) {
	fakeDstack(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Info" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		t.Errorf("quote endpoint continued to %q without verified runtime metadata", r.URL.Path)
	})
	request := httptest.NewRequest(http.MethodGet, "/attestation?reportData=abcd", nil)
	response := httptest.NewRecorder()
	(&Server{}).attestationQuote(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502, got %d: %s", response.Code, response.Body.String())
	}
}

type QuoteEvidenceForTest struct {
	Quote       string `json:"quote"`
	Attestation string `json:"attestation"`
	ReportData  string `json:"report_data"`
}

func TestAttestationInfoSurfacesGuestErrors(t *testing.T) {
	fakeDstack(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"TDX unavailable"}`))
	})
	request := httptest.NewRequest(http.MethodGet, "/info", nil)
	response := httptest.NewRecorder()
	(&Server{}).attestationInfo(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502, got %d", response.Code)
	}
}

func TestAttestationEvidenceDisablesCachingAndSniffing(t *testing.T) {
	response := httptest.NewRecorder()
	writeAttestationEvidence(response, json.RawMessage(`{"quote":"deadbeef"}`))
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}
