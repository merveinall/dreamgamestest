package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------- configuration -----------------------------------------------

const (
	configMapName      = "webhook-namespaces"
	configMapNamespace = "webhook-system"
	configMapKey       = "enabled-namespaces" // comma-separated list, "*" means all
	tlsCertFile        = "/etc/webhook/tls/tls.crt"
	tlsKeyFile         = "/etc/webhook/tls/tls.key"
	cacheRefreshPeriod = 30 * time.Second
)

// ---------- metrics -------------------------------------------------------

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "resource_webhook_requests_total",
		Help: "Total admission requests received, by operation and namespace.",
	}, []string{"operation", "namespace"})

	allowedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "resource_webhook_allowed_total",
		Help: "Total admission requests allowed, by namespace.",
	}, []string{"namespace"})

	deniedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "resource_webhook_denied_total",
		Help: "Total admission requests denied, by namespace and reason.",
	}, []string{"namespace", "reason"})

	durationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "resource_webhook_duration_seconds",
		Help:    "Admission request processing latency in seconds, by operation.",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation"})
)

// ---------- server --------------------------------------------------------

type server struct {
	k8s *kubernetes.Clientset

	mu          sync.RWMutex
	namespaces  []string
	lastRefresh time.Time
}

func main() {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("in-cluster config: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	srv := &server{k8s: client}

	// Metrics server on plain HTTP :8080 — scraped by Prometheus
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", healthz)
		log.Println("metrics server listening on :8080")
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Fatalf("metrics server: %v", err)
		}
	}()

	// Webhook server on TLS :8443
	mux := http.NewServeMux()
	mux.HandleFunc("/validate", srv.handleValidate)
	mux.HandleFunc("/healthz", healthz)

	certFile := env("TLS_CERT_FILE", tlsCertFile)
	keyFile := env("TLS_KEY_FILE", tlsKeyFile)

	log.Println("webhook server listening on :8443")
	if err := http.ListenAndServeTLS(":8443", certFile, keyFile, mux); err != nil {
		log.Fatalf("webhook server: %v", err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------- namespace cache -----------------------------------------------

// enabledNamespaces fetches (and caches for cacheRefreshPeriod) the namespace
// list from the ConfigMap so the webhook doesn't hit the API on every request.
func (s *server) enabledNamespaces(ctx context.Context) []string {
	s.mu.RLock()
	if time.Since(s.lastRefresh) < cacheRefreshPeriod {
		ns := s.namespaces
		s.mu.RUnlock()
		return ns
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.k8s.CoreV1().ConfigMaps(configMapNamespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		log.Printf("WARN: could not fetch configmap %s/%s: %v — keeping previous list", configMapNamespace, configMapName, err)
		return s.namespaces
	}

	var result []string
	for _, part := range strings.Split(cm.Data[configMapKey], ",") {
		if p := strings.TrimSpace(part); p != "" {
			result = append(result, p)
		}
	}

	s.namespaces = result
	s.lastRefresh = time.Now()
	log.Printf("INFO: enabled namespaces refreshed: %v", result)
	return result
}

func (s *server) isEnabled(ctx context.Context, ns string) bool {
	for _, enabled := range s.enabledNamespaces(ctx) {
		if enabled == "*" || enabled == ns {
			return true
		}
	}
	return false
}

// ---------- admission handler --------------------------------------------

func (s *server) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, fmt.Sprintf("cannot parse AdmissionReview: %v", err), http.StatusBadRequest)
		return
	}

	req := review.Request
	start := time.Now()
	requestsTotal.WithLabelValues(string(req.Operation), req.Namespace).Inc()

	response := s.validate(r.Context(), req)
	response.UID = req.UID

	durationSeconds.WithLabelValues(string(req.Operation)).Observe(time.Since(start).Seconds())

	review.Response = response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(review); err != nil {
		log.Printf("ERROR: encoding response: %v", err)
	}
}

func (s *server) validate(ctx context.Context, req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	if !s.isEnabled(ctx, req.Namespace) {
		allowedTotal.WithLabelValues(req.Namespace).Inc()
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	var deploy appsv1.Deployment
	if err := json.Unmarshal(req.Object.Raw, &deploy); err != nil {
		return deny(req.Namespace, "parse_error",
			fmt.Sprintf("cannot parse Deployment object: %v", err))
	}

	var violations []string
	for _, c := range deploy.Spec.Template.Spec.Containers {
		if c.Resources.Requests.Cpu().IsZero() {
			violations = append(violations,
				fmt.Sprintf("container %q: cpu request is missing", c.Name))
		}
		if c.Resources.Requests.Memory().IsZero() {
			violations = append(violations,
				fmt.Sprintf("container %q: memory request is missing", c.Name))
		}
	}

	if len(violations) > 0 {
		return deny(req.Namespace, "missing_resource_requests", strings.Join(violations, "; "))
	}

	allowedTotal.WithLabelValues(req.Namespace).Inc()
	return &admissionv1.AdmissionResponse{Allowed: true}
}

func deny(ns, reason, message string) *admissionv1.AdmissionResponse {
	deniedTotal.WithLabelValues(ns, reason).Inc()
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result:  &metav1.Status{Message: message},
	}
}
