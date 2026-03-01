package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"

	"github.com/example/k8s-api/handlers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker so that gorilla/websocket can upgrade
// connections even when the ResponseWriter is wrapped by loggingMiddleware.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := newResponseWriter(w)
		next.ServeHTTP(rw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func buildConfig() (*rest.Config, error) {
	// Try in-cluster config first (when running inside a Pod)
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}

	// Fall back to kubeconfig file
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func main() {
	// Self health-check mode: used by the Docker/K8s HEALTHCHECK instruction.
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/health", port))
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := buildConfig()
	if err != nil {
		slog.Error("failed to build kubeconfig", "error", err)
		os.Exit(1)
	}

	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}

	metricsClient, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		slog.Warn("metrics server unavailable, pod metrics endpoint will return 503", "error", err)
		metricsClient = nil
	}

	h := handlers.New(k8sClient, metricsClient, cfg)

	r := mux.NewRouter()

	// Health
	r.HandleFunc("/health", h.Health).Methods(http.MethodGet)

	// Pods — literal routes first so gorilla/mux doesn't swallow them as path vars
	r.HandleFunc("/pods", h.ListPods).Methods(http.MethodGet)
	r.HandleFunc("/pods", h.CreatePod).Methods(http.MethodPost)
	r.HandleFunc("/pods/exec/ws", h.ExecPodWS) // WebSocket: no Methods constraint
	r.HandleFunc("/pods/{namespace}/{name}/logs", h.GetPodLogs).Methods(http.MethodGet)
	r.HandleFunc("/pods/{namespace}/{name}/metrics", h.GetPodMetrics).Methods(http.MethodGet)
	r.HandleFunc("/pods/{namespace}/{name}/restart", h.RestartPod).Methods(http.MethodPost)
	r.HandleFunc("/pods/{namespace}/{name}/exec", h.CreateExecSession).Methods(http.MethodPost)
	r.HandleFunc("/pods/{namespace}/{name}", h.GetPod).Methods(http.MethodGet)
	r.HandleFunc("/pods/{namespace}/{name}", h.DeletePod).Methods(http.MethodDelete)

	// Deployments
	r.HandleFunc("/deployments", h.ListDeployments).Methods(http.MethodGet)
	r.HandleFunc("/deployments", h.CreateDeployment).Methods(http.MethodPost)
	r.HandleFunc("/deployments/{namespace}/{name}/scale", h.ScaleDeployment).Methods(http.MethodPost)
	r.HandleFunc("/deployments/{namespace}/{name}/restart", h.RestartDeployment).Methods(http.MethodPost)
	r.HandleFunc("/deployments/{namespace}/{name}", h.GetDeployment).Methods(http.MethodGet)
	r.HandleFunc("/deployments/{namespace}/{name}", h.DeleteDeployment).Methods(http.MethodDelete)

	// Services
	r.HandleFunc("/services", h.ListServices).Methods(http.MethodGet)
	r.HandleFunc("/services", h.CreateService).Methods(http.MethodPost)
	r.HandleFunc("/services/{namespace}/{name}", h.GetService).Methods(http.MethodGet)
	r.HandleFunc("/services/{namespace}/{name}", h.DeleteService).Methods(http.MethodDelete)

	// Namespaces
	r.HandleFunc("/namespaces", h.ListNamespaces).Methods(http.MethodGet)
	r.HandleFunc("/namespaces", h.CreateNamespace).Methods(http.MethodPost)
	r.HandleFunc("/namespaces/{name}", h.GetNamespace).Methods(http.MethodGet)
	r.HandleFunc("/namespaces/{name}", h.DeleteNamespace).Methods(http.MethodDelete)

	// ConfigMaps
	r.HandleFunc("/configmaps", h.ListConfigMaps).Methods(http.MethodGet)
	r.HandleFunc("/configmaps", h.CreateConfigMap).Methods(http.MethodPost)
	r.HandleFunc("/configmaps/{namespace}/{name}", h.GetConfigMap).Methods(http.MethodGet)
	r.HandleFunc("/configmaps/{namespace}/{name}", h.DeleteConfigMap).Methods(http.MethodDelete)

	// PersistentVolumeClaims
	r.HandleFunc("/pvcs", h.ListPVCs).Methods(http.MethodGet)
	r.HandleFunc("/pvcs", h.CreatePVC).Methods(http.MethodPost)
	r.HandleFunc("/pvcs/{namespace}/{name}", h.GetPVC).Methods(http.MethodGet)
	r.HandleFunc("/pvcs/{namespace}/{name}", h.DeletePVC).Methods(http.MethodDelete)

	// Nodes
	r.HandleFunc("/nodes", h.ListNodes).Methods(http.MethodGet)
	r.HandleFunc("/nodes/{name}", h.GetNode).Methods(http.MethodGet)

	// Manifest apply/delete (for lab provisioning from YAML manifests)
	r.HandleFunc("/manifests/apply", h.ApplyManifests).Methods(http.MethodPost)
	r.HandleFunc("/manifests/{namespace}/{kind}/{name}", h.DeleteManifestResource).Methods(http.MethodDelete)

	// System / Cluster
	r.HandleFunc("/system/info", h.ClusterInfo).Methods(http.MethodGet)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      loggingMiddleware(r),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("server shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
