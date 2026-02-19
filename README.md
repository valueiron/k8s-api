# k8s-api

A production-ready Go REST API that provides a simplified, high-level interface to the Kubernetes API using [client-go](https://github.com/kubernetes/client-go). It mirrors the design of the companion `docker-api` project and supports both in-cluster (Pod service-account) and out-of-cluster (kubeconfig) authentication.

---

## Features

| Resource | Operations |
|---|---|
| **Pods** | List, inspect, delete, logs, per-container metrics, restart |
| **Deployments** | List, create, inspect, scale, rolling-restart, delete |
| **Services** | List, create, inspect, delete |
| **Namespaces** | List, create, inspect, delete |
| **ConfigMaps** | List, create, inspect, delete |
| **PersistentVolumeClaims** | List, create, inspect, delete |
| **Nodes** | List, inspect (capacity, allocatable, conditions) |
| **Cluster** | Server version, node/namespace/pod counts |

---

## Technology Stack

| Component | Choice |
|---|---|
| Language | Go 1.24 |
| HTTP Router | gorilla/mux |
| Kubernetes SDK | k8s.io/client-go v0.32 |
| Metrics | k8s.io/metrics v0.32 (optional) |
| Logging | log/slog (structured JSON) |
| Runtime image | gcr.io/distroless/static-debian12:nonroot |

---

## API Endpoints

### Health
```
GET  /health
```

### Pods
```
GET    /pods                              # list all pods (?namespace=)
GET    /pods/{namespace}/{name}           # inspect a pod
DELETE /pods/{namespace}/{name}           # delete a pod (?force=true)
GET    /pods/{namespace}/{name}/logs      # stream logs (?container=, ?tail=100, ?since=RFC3339)
GET    /pods/{namespace}/{name}/metrics   # CPU/memory usage (requires metrics-server)
POST   /pods/{namespace}/{name}/restart   # delete pod, let controller recreate it
```

### Deployments
```
GET    /deployments                             # list all deployments (?namespace=)
POST   /deployments                             # create a deployment
GET    /deployments/{namespace}/{name}          # inspect a deployment
DELETE /deployments/{namespace}/{name}          # delete a deployment
POST   /deployments/{namespace}/{name}/scale    # scale replicas
POST   /deployments/{namespace}/{name}/restart  # rolling restart
```

### Services
```
GET    /services                        # list all services (?namespace=)
POST   /services                        # create a service
GET    /services/{namespace}/{name}     # inspect a service
DELETE /services/{namespace}/{name}     # delete a service
```

### Namespaces
```
GET    /namespaces          # list all namespaces
POST   /namespaces          # create a namespace
GET    /namespaces/{name}   # inspect a namespace
DELETE /namespaces/{name}   # delete a namespace
```

### ConfigMaps
```
GET    /configmaps                      # list all configmaps (?namespace=)
POST   /configmaps                      # create a configmap
GET    /configmaps/{namespace}/{name}   # inspect a configmap
DELETE /configmaps/{namespace}/{name}   # delete a configmap
```

### PersistentVolumeClaims
```
GET    /pvcs                      # list all PVCs (?namespace=)
POST   /pvcs                      # create a PVC
GET    /pvcs/{namespace}/{name}   # inspect a PVC
DELETE /pvcs/{namespace}/{name}   # delete a PVC
```

### Nodes
```
GET /nodes          # list all nodes
GET /nodes/{name}   # inspect a node
```

### System
```
GET /system/info   # cluster version, node/namespace/pod counts
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `KUBECONFIG` | `~/.kube/config` | Path to kubeconfig (ignored when in-cluster) |

When running inside a Kubernetes Pod the API automatically uses the Pod's service-account token (in-cluster config). `KUBECONFIG` is only needed for out-of-cluster deployments.

---

## Getting Started

### Prerequisites

- Go 1.24+
- Access to a Kubernetes cluster (local kubeconfig or in-cluster)
- `go mod tidy` to download dependencies

### Build and run locally

```bash
cd k8s-api
go mod tidy
go build -o k8s-api .
./k8s-api
```

### Docker Compose (out-of-cluster)

Mounts your local `~/.kube/config` so the API can reach your cluster:

```bash
cp .env.example .env   # adjust if needed
docker compose up --build
```

### Kubernetes deployment (in-cluster)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: k8s-api
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: k8s-api
  template:
    metadata:
      labels:
        app: k8s-api
    spec:
      serviceAccountName: k8s-api   # see RBAC below
      containers:
        - name: k8s-api
          image: k8s-api:latest
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 30
---
apiVersion: v1
kind: Service
metadata:
  name: k8s-api
  namespace: default
spec:
  selector:
    app: k8s-api
  ports:
    - port: 80
      targetPort: 8080
```

### RBAC

The API needs read/write access to the resources it manages. A minimal `ClusterRole` example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: k8s-api
rules:
  - apiGroups: [""]
    resources: [pods, pods/log, services, namespaces, configmaps, persistentvolumeclaims, nodes]
    verbs: [get, list, watch, create, delete]
  - apiGroups: [apps]
    resources: [deployments, deployments/scale]
    verbs: [get, list, watch, create, update, patch, delete]
  - apiGroups: [metrics.k8s.io]
    resources: [pods]
    verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: k8s-api
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: k8s-api
subjects:
  - kind: ServiceAccount
    name: k8s-api
    namespace: default
```

---

## Request / Response

All endpoints return `application/json` except pod logs (`text/plain`).

**Status codes:**

| Code | Meaning |
|---|---|
| `200` | Success (read/update) |
| `201` | Created |
| `204` | Deleted (no body) |
| `400` | Bad request / validation error |
| `404` | Resource not found |
| `500` | Kubernetes API error |
| `503` | metrics-server not available |

**Error body:**
```json
{ "error": "descriptive message" }
```

### Example requests

**List pods in the `kube-system` namespace:**
```bash
curl http://localhost:8080/pods?namespace=kube-system
```

**Create a deployment:**
```bash
curl -X POST http://localhost:8080/deployments \
  -H 'Content-Type: application/json' \
  -d '{"name":"nginx","image":"nginx:latest","replicas":2,"port":80}'
```

**Scale a deployment:**
```bash
curl -X POST http://localhost:8080/deployments/default/nginx/scale \
  -H 'Content-Type: application/json' \
  -d '{"replicas":5}'
```

**Tail pod logs:**
```bash
curl "http://localhost:8080/pods/default/nginx-abc/logs?tail=50&container=nginx"
```

**Create a PVC:**
```bash
curl -X POST http://localhost:8080/pvcs \
  -H 'Content-Type: application/json' \
  -d '{"name":"data","namespace":"default","storage":"10Gi","access_modes":["ReadWriteOnce"]}'
```

---

## Pod Metrics

The `/pods/{namespace}/{name}/metrics` endpoint requires [metrics-server](https://github.com/kubernetes-sigs/metrics-server) to be installed. If it is not available the endpoint returns `503 Service Unavailable`. The response looks like:

```json
{
  "name": "my-pod",
  "namespace": "default",
  "containers": [
    { "name": "app", "cpu_millicores": 42, "memory_mib": 128 }
  ]
}
```

---

## Project Structure

```
k8s-api/
├── main.go                # Entry point, routing, middleware, graceful shutdown
├── handlers/
│   ├── handler.go         # Handler struct, shared helpers (writeJSON, writeError)
│   ├── health.go          # GET /health
│   ├── pods.go            # Pod lifecycle + logs + metrics
│   ├── deployments.go     # Deployment CRUD + scale + rolling restart
│   ├── services.go        # Service CRUD
│   ├── namespaces.go      # Namespace CRUD
│   ├── configmaps.go      # ConfigMap CRUD
│   ├── pvcs.go            # PersistentVolumeClaim CRUD
│   ├── nodes.go           # Node listing and inspection
│   └── system.go          # Cluster-level info
├── openapi.json           # OpenAPI 3.0 specification
├── Dockerfile             # Multi-stage build → distroless runtime
├── docker-compose.yml     # Out-of-cluster development setup
├── .env.example           # Environment variable template
└── README.md
```
