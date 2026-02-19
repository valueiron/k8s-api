package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PVCSummary is a trimmed representation of a Kubernetes PersistentVolumeClaim.
type PVCSummary struct {
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Status           string            `json:"status"`
	StorageClass     string            `json:"storage_class,omitempty"`
	AccessModes      []string          `json:"access_modes,omitempty"`
	Capacity         string            `json:"capacity,omitempty"`
	RequestedStorage string            `json:"requested_storage"`
	VolumeName       string            `json:"volume_name,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// CreatePVCRequest is the body accepted by POST /pvcs.
type CreatePVCRequest struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	StorageClass string            `json:"storage_class,omitempty"`
	AccessModes  []string          `json:"access_modes"`
	Storage      string            `json:"storage"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// ListPVCs returns all PersistentVolumeClaims, optionally filtered by ?namespace=.
func (h *Handler) ListPVCs(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	pvcs, err := h.k8s.CoreV1().PersistentVolumeClaims(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list pvcs", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]PVCSummary, 0, len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		summaries = append(summaries, pvcToSummary(pvc))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetPVC returns full details for a single PersistentVolumeClaim.
func (h *Handler) GetPVC(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	pvc, err := h.k8s.CoreV1().PersistentVolumeClaims(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "pvc not found")
			return
		}
		slog.Error("failed to get pvc", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pvc)
}

// CreatePVC creates a PersistentVolumeClaim.
// Access modes: ReadWriteOnce, ReadOnlyMany, ReadWriteMany, ReadWriteOncePod.
// Storage: Kubernetes quantity string, e.g. "5Gi", "500Mi".
func (h *Handler) CreatePVC(w http.ResponseWriter, r *http.Request) {
	var req CreatePVCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Storage == "" || len(req.AccessModes) == 0 {
		writeError(w, http.StatusBadRequest, "name, storage, and access_modes are required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	storageQty, err := resource.ParseQuantity(req.Storage)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid storage quantity: "+err.Error())
		return
	}

	accessModes := make([]corev1.PersistentVolumeAccessMode, 0, len(req.AccessModes))
	for _, am := range req.AccessModes {
		accessModes = append(accessModes, corev1.PersistentVolumeAccessMode(am))
	}

	spec := corev1.PersistentVolumeClaimSpec{
		AccessModes: accessModes,
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storageQty,
			},
		},
	}
	if req.StorageClass != "" {
		spec.StorageClassName = &req.StorageClass
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    req.Labels,
		},
		Spec: spec,
	}

	created, err := h.k8s.CoreV1().PersistentVolumeClaims(req.Namespace).Create(r.Context(), pvc, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create pvc", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pvcToSummary(*created))
}

// DeletePVC removes a PersistentVolumeClaim.
func (h *Handler) DeletePVC(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	if err := h.k8s.CoreV1().PersistentVolumeClaims(namespace).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "pvc not found")
			return
		}
		slog.Error("failed to delete pvc", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pvcToSummary(pvc corev1.PersistentVolumeClaim) PVCSummary {
	accessModes := make([]string, 0, len(pvc.Spec.AccessModes))
	for _, am := range pvc.Spec.AccessModes {
		accessModes = append(accessModes, string(am))
	}

	capacity := ""
	if c, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		capacity = c.String()
	}

	requested := ""
	if r, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		requested = r.String()
	}

	storageClass := ""
	if pvc.Spec.StorageClassName != nil {
		storageClass = *pvc.Spec.StorageClassName
	}

	return PVCSummary{
		Name:             pvc.Name,
		Namespace:        pvc.Namespace,
		Status:           string(pvc.Status.Phase),
		StorageClass:     storageClass,
		AccessModes:      accessModes,
		Capacity:         capacity,
		RequestedStorage: requested,
		VolumeName:       pvc.Spec.VolumeName,
		Labels:           pvc.Labels,
		CreatedAt:        pvc.CreationTimestamp.Time,
	}
}
