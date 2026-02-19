package handlers

import "net/http"

// Health returns a 200 OK with {"status":"ok"} for liveness/readiness probes.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
