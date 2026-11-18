// attestation.go: public dstack evidence endpoints for external, browser-side verification.
package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxAttestationResponseBytes = 32 << 20

func dstackRPC(ctx context.Context, method string, payload []byte) (json.RawMessage, error) {
	socket := os.Getenv("DSTACK_SOCKET")
	if socket == "" {
		socket = "/var/run/dstack.sock"
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dstack/"+method, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dstack guest agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAttestationResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAttestationResponseBytes {
		return nil, fmt.Errorf("dstack response exceeds %d bytes", maxAttestationResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dstack %s returned HTTP %d", method, resp.StatusCode)
	}
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("dstack %s returned malformed JSON: %w", method, err)
	}
	if envelope.Error != "" {
		return nil, fmt.Errorf("dstack %s: %s", method, envelope.Error)
	}
	return json.RawMessage(body), nil
}

func writeAttestationEvidence(w http.ResponseWriter, evidence json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(evidence)
}

func (s *Server) attestationQuote(w http.ResponseWriter, r *http.Request) {
	reportData := strings.TrimPrefix(r.URL.Query().Get("reportData"), "0x")
	if reportData == "" {
		reportData = strings.Repeat("0", 128)
	}
	if len(reportData) > 128 || len(reportData)%2 != 0 {
		writeErr(w, 400, "reportData must be an even-length hex string of at most 64 bytes")
		return
	}
	if _, err := hex.DecodeString(reportData); err != nil {
		writeErr(w, 400, "reportData must be hexadecimal")
		return
	}
	info, err := dstackRPC(r.Context(), "Info", []byte("{}"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	var metadata struct {
		CloudVendor string `json:"cloud_vendor"`
	}
	if err := json.Unmarshal(info, &metadata); err != nil {
		writeErr(w, 502, "dstack Info returned malformed runtime metadata")
		return
	}
	payload, _ := json.Marshal(map[string]string{"report_data": reportData})
	evidence, err := dstackRPC(r.Context(), "GetQuote", payload)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if metadata.CloudVendor == "Google" {
		attestation, attestErr := dstackRPC(r.Context(), "Attest", payload)
		if attestErr != nil {
			writeErr(w, 502, attestErr.Error())
			return
		}
		var full struct {
			Attestation json.RawMessage `json:"attestation"`
		}
		if err := json.Unmarshal(attestation, &full); err != nil || len(full.Attestation) == 0 {
			writeErr(w, 502, "dstack Attest returned no attestation evidence")
			return
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(evidence, &fields); err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		fields["attestation"] = full.Attestation
		evidence, _ = json.Marshal(fields)
	}
	writeAttestationEvidence(w, evidence)
}

func (s *Server) attestationInfo(w http.ResponseWriter, r *http.Request) {
	evidence, err := dstackRPC(r.Context(), "Info", []byte("{}"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeAttestationEvidence(w, evidence)
}
