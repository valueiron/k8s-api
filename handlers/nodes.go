package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeSummary is a trimmed representation of a Kubernetes Node.
type NodeSummary struct {
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	Roles             []string          `json:"roles"`
	KubeletVersion    string            `json:"kubelet_version"`
	OSImage           string            `json:"os_image"`
	ContainerRuntime  string            `json:"container_runtime"`
	Architecture      string            `json:"architecture"`
	CPUCapacity       string            `json:"cpu_capacity"`
	MemoryCapacity    string            `json:"memory_capacity"`
	CPUAllocatable    string            `json:"cpu_allocatable"`
	MemoryAllocatable string            `json:"memory_allocatable"`
	Labels            map[string]string `json:"labels,omitempty"`
	Conditions        []NodeCondition   `json:"conditions"`
	Addresses         []NodeAddress     `json:"addresses"`
	CreatedAt         time.Time         `json:"created_at"`
}

// NodeCondition represents a single condition of a node.
type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// NodeAddress represents a network address of a node.
type NodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// ListNodes returns all nodes in the cluster.
func (h *Handler) ListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.k8s.CoreV1().Nodes().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list nodes", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]NodeSummary, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		summaries = append(summaries, nodeToSummary(node))
	}
	writeJSON(w, http.StatusOK, summaries)
}

// GetNode returns full details for a single node.
func (h *Handler) GetNode(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]

	node, err := h.k8s.CoreV1().Nodes().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		slog.Error("failed to get node", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nodeToSummary(*node))
}

func nodeToSummary(node corev1.Node) NodeSummary {
	status := "Unknown"
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
			}
		}
	}

	// Derive roles from well-known labels
	roleSet := map[string]struct{}{}
	for label := range node.Labels {
		switch label {
		case "node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master":
			roleSet["control-plane"] = struct{}{}
		case "node-role.kubernetes.io/worker":
			roleSet["worker"] = struct{}{}
		}
	}
	roles := make([]string, 0, len(roleSet))
	for role := range roleSet {
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		roles = []string{"worker"}
	}

	conditions := make([]NodeCondition, 0, len(node.Status.Conditions))
	for _, c := range node.Status.Conditions {
		conditions = append(conditions, NodeCondition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Message: c.Message,
			Reason:  c.Reason,
		})
	}

	addresses := make([]NodeAddress, 0, len(node.Status.Addresses))
	for _, a := range node.Status.Addresses {
		addresses = append(addresses, NodeAddress{
			Type:    string(a.Type),
			Address: a.Address,
		})
	}

	return NodeSummary{
		Name:              node.Name,
		Status:            status,
		Roles:             roles,
		KubeletVersion:    node.Status.NodeInfo.KubeletVersion,
		OSImage:           node.Status.NodeInfo.OSImage,
		ContainerRuntime:  node.Status.NodeInfo.ContainerRuntimeVersion,
		Architecture:      node.Status.NodeInfo.Architecture,
		CPUCapacity:       node.Status.Capacity.Cpu().String(),
		MemoryCapacity:    node.Status.Capacity.Memory().String(),
		CPUAllocatable:    node.Status.Allocatable.Cpu().String(),
		MemoryAllocatable: node.Status.Allocatable.Memory().String(),
		Labels:            node.Labels,
		Conditions:        conditions,
		Addresses:         addresses,
		CreatedAt:         node.CreationTimestamp.Time,
	}
}
