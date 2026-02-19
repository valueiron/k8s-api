package handlers

import (
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterInfoResponse summarises high-level cluster metadata.
type ClusterInfoResponse struct {
	ServerVersion  string `json:"server_version"`
	GitCommit      string `json:"git_commit"`
	Platform       string `json:"platform"`
	GoVersion      string `json:"go_version"`
	NodeCount      int    `json:"node_count"`
	NamespaceCount int    `json:"namespace_count"`
	PodCount       int    `json:"pod_count"`
}

// ClusterInfo returns cluster-wide summary information.
func (h *Handler) ClusterInfo(w http.ResponseWriter, r *http.Request) {
	version, err := h.k8s.Discovery().ServerVersion()
	if err != nil {
		slog.Error("failed to get server version", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	nodes, err := h.k8s.CoreV1().Nodes().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list nodes", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	namespaces, err := h.k8s.CoreV1().Namespaces().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list namespaces", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// List pods across all namespaces
	pods, err := h.k8s.CoreV1().Pods("").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list pods", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ClusterInfoResponse{
		ServerVersion:  version.GitVersion,
		GitCommit:      version.GitCommit,
		Platform:       version.Platform,
		GoVersion:      version.GoVersion,
		NodeCount:      len(nodes.Items),
		NamespaceCount: len(namespaces.Items),
		PodCount:       len(pods.Items),
	})
}
