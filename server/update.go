// Legacy update endpoints report that updates must be installed by redeployment.
// This build contains no downloader, update cache, or binary replacement path.
package server

import (
	"fmt"
	"net/http"
)

const selfUpdateDisabledReason = "Self-update is disabled in this build; updates are managed by redeploying the container image."

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"disabled": true,
		"current":  BuildVersion,
		"reason":   selfUpdateDisabledReason,
	})
}

func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, selfUpdateDisabledReason)
}

func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, selfUpdateDisabledReason)
}

// Return one terminal compatibility event rather than keeping an idle updater alive.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "data: {\"phase\":\"disabled\",\"disabled\":true,\"percent\":-1}\n\n")
}
