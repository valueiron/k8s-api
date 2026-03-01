package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodSummary is a trimmed representation of a Kubernetes Pod.
type PodSummary struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Phase      string            `json:"phase"`
	NodeName   string            `json:"node_name"`
	PodIP      string            `json:"pod_ip"`
	StartTime  *time.Time        `json:"start_time,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Containers []ContainerStatus `json:"containers"`
}

// ContainerStatus describes the runtime state of a container inside a pod.
type ContainerStatus struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	State    string `json:"state"`
}

// CreatePod handles POST /pods.
// Body: {"name": "<pod-name>", "namespace": "<ns>", "image": "<image>"}
// Creates a pod with sleep infinity as its command so it stays alive for exec sessions.
// Returns: {"name": "<name>", "namespace": "<ns>"}
func (h *Handler) CreatePod(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Image     string `json:"image"`
	}
	json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
	if body.Name == "" || body.Image == "" {
		writeError(w, http.StatusBadRequest, "body must include 'name' and 'image'")
		return
	}
	ns := body.Namespace
	if ns == "" {
		ns = "default"
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      body.Name,
			Namespace: ns,
			Labels:    map[string]string{"app": "lab-shell"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "shell",
					Image:   body.Image,
					Command: []string{"sleep", "infinity"},
				},
			},
		},
	}

	created, err := h.k8s.CoreV1().Pods(ns).Create(r.Context(), pod, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create pod", "name", body.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("pod created", "name", created.Name, "namespace", created.Namespace)
	writeJSON(w, http.StatusOK, map[string]string{"name": created.Name, "namespace": created.Namespace})
}

// ListPods returns all pods, optionally filtered by ?namespace=.
func (h *Handler) ListPods(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	pods, err := h.k8s.CoreV1().Pods(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list pods", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]PodSummary, 0, len(pods.Items))
	for _, pod := range pods.Items {
		summaries = append(summaries, podToSummary(pod))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetPod returns full details for a single pod.
func (h *Handler) GetPod(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	pod, err := h.k8s.CoreV1().Pods(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "pod not found")
			return
		}
		slog.Error("failed to get pod", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pod)
}

// DeletePod deletes a pod. Pass ?force=true to set gracePeriod=0.
func (h *Handler) DeletePod(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	opts := metav1.DeleteOptions{}
	if f := r.URL.Query().Get("force"); f == "1" || f == "true" {
		gracePeriod := int64(0)
		opts.GracePeriodSeconds = &gracePeriod
	}

	if err := h.k8s.CoreV1().Pods(namespace).Delete(r.Context(), name, opts); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "pod not found")
			return
		}
		slog.Error("failed to delete pod", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetPodLogs streams pod logs as text/plain.
// Query params: container, tail (default 100), since (RFC3339).
func (h *Handler) GetPodLogs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	tail := int64(100)
	if t, err := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64); err == nil && t > 0 {
		tail = t
	}

	opts := &corev1.PodLogOptions{
		Container: r.URL.Query().Get("container"),
		TailLines: &tail,
	}

	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			mt := metav1.NewTime(t)
			opts.SinceTime = &mt
		}
	}

	stream, err := h.k8s.CoreV1().Pods(namespace).GetLogs(name, opts).Stream(r.Context())
	if err != nil {
		slog.Error("failed to stream pod logs", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, stream); err != nil {
		slog.Error("log stream interrupted", "error", err)
	}
}

// GetPodMetrics returns CPU and memory usage for each container in a pod.
// Requires metrics-server to be installed in the cluster.
func (h *Handler) GetPodMetrics(w http.ResponseWriter, r *http.Request) {
	if h.metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics server not available")
		return
	}

	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	pm, err := h.metrics.MetricsV1beta1().PodMetricses(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Distinguish between "metrics-server not installed" (API group missing)
			// and "no metrics collected yet for this pod" (pod-specific 404).
			if statusErr, ok := err.(*k8serrors.StatusError); ok &&
				strings.Contains(statusErr.ErrStatus.Message, "the server could not find the requested resource") {
				writeError(w, http.StatusServiceUnavailable, "metrics-server is not installed in this cluster")
				return
			}
			writeError(w, http.StatusNotFound, "pod metrics not found (metrics may not be collected yet)")
			return
		}
		slog.Error("failed to get pod metrics", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type containerMetrics struct {
		Name        string `json:"name"`
		CPUMilliCores int64 `json:"cpu_millicores"`
		MemoryMiB   int64 `json:"memory_mib"`
	}
	type podMetricsResp struct {
		Name       string             `json:"name"`
		Namespace  string             `json:"namespace"`
		Containers []containerMetrics `json:"containers"`
	}

	resp := podMetricsResp{Name: pm.Name, Namespace: pm.Namespace}
	for _, c := range pm.Containers {
		resp.Containers = append(resp.Containers, containerMetrics{
			Name:        c.Name,
			CPUMilliCores: c.Usage.Cpu().MilliValue(),
			MemoryMiB:   c.Usage.Memory().Value() / (1024 * 1024),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// RestartPod deletes the pod so its controller (e.g. Deployment) recreates it.
func (h *Handler) RestartPod(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	if err := h.k8s.CoreV1().Pods(namespace).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "pod not found")
			return
		}
		slog.Error("failed to restart pod", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pod deleted, controller will recreate it"})
}

func podToSummary(pod corev1.Pod) PodSummary {
	s := PodSummary{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		Phase:     string(pod.Status.Phase),
		NodeName:  pod.Spec.NodeName,
		PodIP:     pod.Status.PodIP,
		Labels:    pod.Labels,
	}

	if pod.Status.StartTime != nil {
		t := pod.Status.StartTime.Time
		s.StartTime = &t
	}

	// Build a quick lookup: container name → image from spec
	specImages := make(map[string]string, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		specImages[c.Name] = c.Image
	}

	for _, cs := range pod.Status.ContainerStatuses {
		state := "unknown"
		switch {
		case cs.State.Running != nil:
			state = "running"
		case cs.State.Waiting != nil:
			state = fmt.Sprintf("waiting: %s", cs.State.Waiting.Reason)
		case cs.State.Terminated != nil:
			state = fmt.Sprintf("terminated: %s", cs.State.Terminated.Reason)
		}
		s.Containers = append(s.Containers, ContainerStatus{
			Name:     cs.Name,
			Image:    specImages[cs.Name],
			Ready:    cs.Ready,
			Restarts: cs.RestartCount,
			State:    state,
		})
	}

	// Populate containers from spec when status is not yet available
	if len(s.Containers) == 0 {
		for _, c := range pod.Spec.Containers {
			s.Containers = append(s.Containers, ContainerStatus{
				Name:  c.Name,
				Image: c.Image,
				State: "pending",
			})
		}
	}
	return s
}
