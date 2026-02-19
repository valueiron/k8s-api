package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Handler holds shared Kubernetes clients used by all route handlers.
type Handler struct {
	k8s     *kubernetes.Clientset
	metrics *metricsclient.Clientset
}

// New creates a Handler. metricsClient may be nil when the metrics-server
// is not installed in the cluster; affected endpoints return 503.
func New(k8s *kubernetes.Clientset, metrics *metricsclient.Clientset) *Handler {
	return &Handler{k8s: k8s, metrics: metrics}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
