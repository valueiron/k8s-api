package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DeploymentSummary is a trimmed representation of a Kubernetes Deployment.
type DeploymentSummary struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Replicas          int32             `json:"replicas"`
	ReadyReplicas     int32             `json:"ready_replicas"`
	AvailableReplicas int32             `json:"available_replicas"`
	UpdatedReplicas   int32             `json:"updated_replicas"`
	Image             string            `json:"image"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	Conditions        []string          `json:"conditions,omitempty"`
}

// CreateDeploymentRequest is the body accepted by POST /deployments.
type CreateDeploymentRequest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Replicas  int32             `json:"replicas"`
	Image     string            `json:"image"`
	Labels    map[string]string `json:"labels,omitempty"`
	Port      int32             `json:"port,omitempty"`
	Env       []EnvVar          `json:"env,omitempty"`
}

// EnvVar is a simple name/value pair for container environment variables.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ScaleRequest is the body accepted by POST /deployments/{ns}/{name}/scale.
type ScaleRequest struct {
	Replicas int32 `json:"replicas"`
}

// ListDeployments returns all deployments, optionally filtered by ?namespace=.
func (h *Handler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	deployments, err := h.k8s.AppsV1().Deployments(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list deployments", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]DeploymentSummary, 0, len(deployments.Items))
	for _, d := range deployments.Items {
		summaries = append(summaries, deploymentToSummary(d))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetDeployment returns full details for a single deployment.
func (h *Handler) GetDeployment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	d, err := h.k8s.AppsV1().Deployments(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		slog.Error("failed to get deployment", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// CreateDeployment creates a deployment from a simplified request body.
func (h *Handler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Image == "" {
		writeError(w, http.StatusBadRequest, "name and image are required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}
	if req.Labels == nil {
		req.Labels = map[string]string{"app": req.Name}
	}

	replicas := req.Replicas

	envVars := make([]corev1.EnvVar, 0, len(req.Env))
	for _, e := range req.Env {
		envVars = append(envVars, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}

	var containerPorts []corev1.ContainerPort
	if req.Port > 0 {
		containerPorts = []corev1.ContainerPort{{ContainerPort: req.Port}}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    req.Labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: req.Labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: req.Labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  req.Name,
							Image: req.Image,
							Ports: containerPorts,
							Env:   envVars,
						},
					},
				},
			},
		},
	}

	created, err := h.k8s.AppsV1().Deployments(req.Namespace).Create(r.Context(), deployment, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create deployment", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, deploymentToSummary(*created))
}

// DeleteDeployment removes a deployment.
func (h *Handler) DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	if err := h.k8s.AppsV1().Deployments(namespace).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		slog.Error("failed to delete deployment", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ScaleDeployment sets the replica count for a deployment.
func (h *Handler) ScaleDeployment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	var req ScaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Replicas < 0 {
		writeError(w, http.StatusBadRequest, "replicas must be >= 0")
		return
	}

	scale, err := h.k8s.AppsV1().Deployments(namespace).GetScale(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		slog.Error("failed to get deployment scale", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	scale.Spec.Replicas = req.Replicas
	updated, err := h.k8s.AppsV1().Deployments(namespace).UpdateScale(r.Context(), name, scale, metav1.UpdateOptions{})
	if err != nil {
		slog.Error("failed to scale deployment", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      name,
		"namespace": namespace,
		"replicas":  updated.Spec.Replicas,
	})
}

// RestartDeployment triggers a rolling restart by updating a pod template annotation.
func (h *Handler) RestartDeployment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	)

	if _, err := h.k8s.AppsV1().Deployments(namespace).Patch(
		r.Context(), name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		slog.Error("failed to restart deployment", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolling restart initiated"})
}

func deploymentToSummary(d appsv1.Deployment) DeploymentSummary {
	image := ""
	if len(d.Spec.Template.Spec.Containers) > 0 {
		image = d.Spec.Template.Spec.Containers[0].Image
	}
	replicas := int32(0)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	conditions := make([]string, 0, len(d.Status.Conditions))
	for _, c := range d.Status.Conditions {
		conditions = append(conditions, fmt.Sprintf("%s=%s", c.Type, c.Status))
	}
	return DeploymentSummary{
		Name:              d.Name,
		Namespace:         d.Namespace,
		Replicas:          replicas,
		ReadyReplicas:     d.Status.ReadyReplicas,
		AvailableReplicas: d.Status.AvailableReplicas,
		UpdatedReplicas:   d.Status.UpdatedReplicas,
		Image:             image,
		Labels:            d.Labels,
		CreatedAt:         d.CreationTimestamp.Time,
		Conditions:        conditions,
	}
}
