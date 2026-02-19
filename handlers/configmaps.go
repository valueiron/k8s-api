package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigMapSummary is a trimmed representation of a Kubernetes ConfigMap.
type ConfigMapSummary struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Keys      []string          `json:"keys,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// CreateConfigMapRequest is the body accepted by POST /configmaps.
type CreateConfigMapRequest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Data      map[string]string `json:"data,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ListConfigMaps returns all configmaps, optionally filtered by ?namespace=.
func (h *Handler) ListConfigMaps(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	cms, err := h.k8s.CoreV1().ConfigMaps(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list configmaps", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]ConfigMapSummary, 0, len(cms.Items))
	for _, cm := range cms.Items {
		summaries = append(summaries, configMapToSummary(cm))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetConfigMap returns full details for a single configmap including all data keys.
func (h *Handler) GetConfigMap(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	cm, err := h.k8s.CoreV1().ConfigMaps(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "configmap not found")
			return
		}
		slog.Error("failed to get configmap", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cm)
}

// CreateConfigMap creates a configmap from a simplified request body.
func (h *Handler) CreateConfigMap(w http.ResponseWriter, r *http.Request) {
	var req CreateConfigMapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    req.Labels,
		},
		Data: req.Data,
	}

	created, err := h.k8s.CoreV1().ConfigMaps(req.Namespace).Create(r.Context(), cm, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create configmap", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, configMapToSummary(*created))
}

// DeleteConfigMap removes a configmap.
func (h *Handler) DeleteConfigMap(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	if err := h.k8s.CoreV1().ConfigMaps(namespace).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "configmap not found")
			return
		}
		slog.Error("failed to delete configmap", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func configMapToSummary(cm corev1.ConfigMap) ConfigMapSummary {
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	return ConfigMapSummary{
		Name:      cm.Name,
		Namespace: cm.Namespace,
		Keys:      keys,
		Labels:    cm.Labels,
		CreatedAt: cm.CreationTimestamp.Time,
	}
}
