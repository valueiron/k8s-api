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
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ServiceSummary is a trimmed representation of a Kubernetes Service.
type ServiceSummary struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Type       string            `json:"type"`
	ClusterIP  string            `json:"cluster_ip"`
	ExternalIPs []string         `json:"external_ips,omitempty"`
	Ports      []ServicePort     `json:"ports,omitempty"`
	Selector   map[string]string `json:"selector,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// ServicePort describes a port exposed by a Service.
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int32  `json:"port"`
	TargetPort string `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
}

// CreateServiceRequest is the body accepted by POST /services.
type CreateServiceRequest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Type      string            `json:"type"`
	Selector  map[string]string `json:"selector"`
	Labels    map[string]string `json:"labels,omitempty"`
	Ports     []ServicePortSpec `json:"ports"`
}

// ServicePortSpec is a port definition inside CreateServiceRequest.
type ServicePortSpec struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
}

// ListServices returns all services, optionally filtered by ?namespace=.
func (h *Handler) ListServices(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	services, err := h.k8s.CoreV1().Services(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list services", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]ServiceSummary, 0, len(services.Items))
	for _, svc := range services.Items {
		summaries = append(summaries, serviceToSummary(svc))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetService returns full details for a single service.
func (h *Handler) GetService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	svc, err := h.k8s.CoreV1().Services(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		slog.Error("failed to get service", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

// CreateService creates a service from a simplified request body.
func (h *Handler) CreateService(w http.ResponseWriter, r *http.Request) {
	var req CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || len(req.Ports) == 0 {
		writeError(w, http.StatusBadRequest, "name and ports are required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	svcType := corev1.ServiceTypeClusterIP
	switch req.Type {
	case "NodePort":
		svcType = corev1.ServiceTypeNodePort
	case "LoadBalancer":
		svcType = corev1.ServiceTypeLoadBalancer
	case "ExternalName":
		svcType = corev1.ServiceTypeExternalName
	}

	ports := make([]corev1.ServicePort, 0, len(req.Ports))
	for _, p := range req.Ports {
		proto := corev1.ProtocolTCP
		if p.Protocol == "UDP" {
			proto = corev1.ProtocolUDP
		}
		sp := corev1.ServicePort{
			Name:       p.Name,
			Protocol:   proto,
			Port:       p.Port,
			TargetPort: intstr.FromInt(int(p.TargetPort)),
		}
		if p.NodePort > 0 {
			sp.NodePort = p.NodePort
		}
		ports = append(ports, sp)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    req.Labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: req.Selector,
			Ports:    ports,
		},
	}

	created, err := h.k8s.CoreV1().Services(req.Namespace).Create(r.Context(), svc, metav1.CreateOptions{})
	if err != nil {
		slog.Error("failed to create service", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, serviceToSummary(*created))
}

// DeleteService removes a service.
func (h *Handler) DeleteService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	if err := h.k8s.CoreV1().Services(namespace).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		slog.Error("failed to delete service", "namespace", namespace, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func serviceToSummary(svc corev1.Service) ServiceSummary {
	ports := make([]ServicePort, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		ports = append(ports, ServicePort{
			Name:       p.Name,
			Protocol:   string(p.Protocol),
			Port:       p.Port,
			TargetPort: p.TargetPort.String(),
			NodePort:   p.NodePort,
		})
	}
	return ServiceSummary{
		Name:        svc.Name,
		Namespace:   svc.Namespace,
		Type:        string(svc.Spec.Type),
		ClusterIP:   svc.Spec.ClusterIP,
		ExternalIPs: svc.Spec.ExternalIPs,
		Ports:       ports,
		Selector:    svc.Spec.Selector,
		Labels:      svc.Labels,
		CreatedAt:   svc.CreationTimestamp.Time,
	}
}
