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

// NamespaceSummary is a trimmed representation of a Kubernetes Namespace.
type NamespaceSummary struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// CreateNamespaceRequest is the body accepted by POST /namespaces.
type CreateNamespaceRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ListNamespaces returns all namespaces.
func (h *Handler) ListNamespaces(w http.ResponseWriter, r *http.Request) {
	namespaces, err := h.k8s.CoreV1().Namespaces().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list namespaces", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]NamespaceSummary, 0, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		summaries = append(summaries, NamespaceSummary{
			Name:      ns.Name,
			Status:    string(ns.Status.Phase),
			Labels:    ns.Labels,
			CreatedAt: ns.CreationTimestamp.Time,
		})
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetNamespace returns full details for a single namespace.
func (h *Handler) GetNamespace(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]

	ns, err := h.k8s.CoreV1().Namespaces().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "namespace not found")
			return
		}
		slog.Error("failed to get namespace", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

// CreateNamespace creates a new namespace.
func (h *Handler) CreateNamespace(w http.ResponseWriter, r *http.Request) {
	var req CreateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: req.Labels,
		},
	}

	created, err := h.k8s.CoreV1().Namespaces().Create(r.Context(), ns, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create namespace", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, NamespaceSummary{
		Name:      created.Name,
		Status:    string(created.Status.Phase),
		Labels:    created.Labels,
		CreatedAt: created.CreationTimestamp.Time,
	})
}

// DeleteNamespace removes a namespace and all resources within it.
func (h *Handler) DeleteNamespace(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]

	if err := h.k8s.CoreV1().Namespaces().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "namespace not found")
			return
		}
		slog.Error("failed to delete namespace", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
