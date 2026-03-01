package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// kindToGVR maps a Kubernetes Kind to its GroupVersionResource.
// Covers the resource types most commonly used in lab manifests.
var kindToGVR = map[string]schema.GroupVersionResource{
	"Pod":                   {Group: "", Version: "v1", Resource: "pods"},
	"Service":               {Group: "", Version: "v1", Resource: "services"},
	"ConfigMap":             {Group: "", Version: "v1", Resource: "configmaps"},
	"ServiceAccount":        {Group: "", Version: "v1", Resource: "serviceaccounts"},
	"Namespace":             {Group: "", Version: "v1", Resource: "namespaces"},
	"PersistentVolumeClaim": {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	"Deployment":            {Group: "apps", Version: "v1", Resource: "deployments"},
	"StatefulSet":           {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"DaemonSet":             {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"Job":                   {Group: "batch", Version: "v1", Resource: "jobs"},
	"CronJob":               {Group: "batch", Version: "v1", Resource: "cronjobs"},
	"Ingress":               {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	"Role":                  {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	"RoleBinding":           {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	"ClusterRole":           {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	"ClusterRoleBinding":    {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
}

// clusterScopedKinds lists resource kinds that are not namespaced.
var clusterScopedKinds = map[string]bool{
	"Namespace":          true,
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
	"Node":               true,
	"PersistentVolume":   true,
}

// AppliedResource is returned for each resource created via ApplyManifests.
type AppliedResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ApplyManifests handles POST /manifests/apply.
// Body: raw YAML (single or multi-document separated by ---).
// Creates each described resource via the dynamic client.
// Returns: {"applied": [{"kind":"Pod","name":"...","namespace":"..."}]}
func (h *Handler) ApplyManifests(w http.ResponseWriter, r *http.Request) {
	if h.dynamic == nil {
		writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	docs := splitYAMLDocs(body)
	var applied []AppliedResource

	for _, doc := range docs {
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		// Convert YAML → JSON → unstructured map
		jsonBytes, err := yaml.YAMLToJSON(doc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
			return
		}

		var obj map[string]interface{}
		if err := json.Unmarshal(jsonBytes, &obj); err != nil {
			writeError(w, http.StatusBadRequest, "invalid manifest: "+err.Error())
			return
		}

		u := &unstructured.Unstructured{Object: obj}
		kind := u.GetKind()
		name := u.GetName()
		ns := u.GetNamespace()

		gvr, ok := kindToGVR[kind]
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported resource kind %q — add it to kindToGVR", kind))
			return
		}

		var created *unstructured.Unstructured
		if !clusterScopedKinds[kind] {
			if ns == "" {
				ns = "default"
				u.SetNamespace(ns)
			}
			created, err = h.dynamic.Resource(gvr).Namespace(ns).Create(r.Context(), u, metav1.CreateOptions{})
		} else {
			created, err = h.dynamic.Resource(gvr).Create(r.Context(), u, metav1.CreateOptions{})
		}
		if err != nil {
			slog.Error("failed to apply resource", "kind", kind, "name", name, "error", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to apply %s %q: %s", kind, name, err.Error()))
			return
		}

		slog.Info("manifest applied", "kind", created.GetKind(), "name", created.GetName(), "namespace", created.GetNamespace())
		applied = append(applied, AppliedResource{
			Kind:      created.GetKind(),
			Name:      created.GetName(),
			Namespace: created.GetNamespace(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"applied": applied})
}

// DeleteManifestResource handles DELETE /manifests/{namespace}/{kind}/{name}.
// Deletes a single resource identified by its kind, namespace, and name.
// Use namespace "_" for cluster-scoped resources.
// Query param: force=true sets gracePeriod=0.
func (h *Handler) DeleteManifestResource(w http.ResponseWriter, r *http.Request) {
	if h.dynamic == nil {
		writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	vars := mux.Vars(r)
	namespace := vars["namespace"]
	kind := vars["kind"]
	name := vars["name"]

	// Look up GVR, accepting any casing
	gvr, ok := kindToGVR[kind]
	if !ok {
		for k, v := range kindToGVR {
			if strings.EqualFold(k, kind) {
				gvr = v
				ok = true
				break
			}
		}
	}
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported resource kind %q", kind))
		return
	}

	opts := metav1.DeleteOptions{}
	if f := r.URL.Query().Get("force"); f == "1" || f == "true" {
		gracePeriod := int64(0)
		opts.GracePeriodSeconds = &gracePeriod
	}

	var err error
	if !clusterScopedKinds[kind] {
		if namespace == "" || namespace == "_" {
			namespace = "default"
		}
		err = h.dynamic.Resource(gvr).Namespace(namespace).Delete(r.Context(), name, opts)
	} else {
		err = h.dynamic.Resource(gvr).Delete(r.Context(), name, opts)
	}
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		slog.Error("failed to delete manifest resource", "kind", kind, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// splitYAMLDocs splits a multi-document YAML byte slice on --- separators.
func splitYAMLDocs(data []byte) [][]byte {
	var docs [][]byte
	// Handle leading --- separator
	data = bytes.TrimPrefix(data, []byte("---\n"))
	data = bytes.TrimPrefix(data, []byte("---"))
	for _, doc := range bytes.Split(data, []byte("\n---")) {
		doc = bytes.TrimSpace(doc)
		// Strip any remaining leading --- that may appear after splitting
		doc = bytes.TrimPrefix(doc, []byte("---"))
		doc = bytes.TrimSpace(doc)
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs
}
