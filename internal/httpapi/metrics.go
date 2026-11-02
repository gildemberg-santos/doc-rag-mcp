package httpapi

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// reqCounter conta requisições HTTP por endpoint, método e status.
	reqCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "doc_rag_http_requests_total",
			Help: "Total de requisições HTTP por endpoint, método e status.",
		},
		[]string{"method", "endpoint", "status"},
	)

	// reqLatency mede a duração das requisições HTTP por endpoint.
	reqLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "doc_rag_http_request_duration_seconds",
			Help:  "Duração das requisições HTTP em segundos.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	// pointsGauge expõe o total de pontos indexados por coleção e projeto.
	pointsGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "doc_rag_qdrant_points_total",
			Help: "Total de pontos indexados no Qdrant, por coleção e projeto.",
		},
		[]string{"collection", "project"},
	)

	// upGauge indica se o servidor está respondendo (1 = up).
	upGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "doc_rag_up",
			Help: "1 quando o servidor doc-rag-mcp está respondendo.",
		},
	)
)

func init() {
	prometheus.MustRegister(reqCounter, reqLatency, pointsGauge, upGauge)
}

// MetricsHandler retorna um http.Handler que expõe as métricas no formato
// Prometheus (GET /metrics).
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// RecordRequest incrementa o contador de requisições e observa a latência.
// Deve ser chamado pelo middleware de métricas após cada requisição.
func RecordRequest(method, endpoint string, status int, d time.Duration) {
	reqCounter.WithLabelValues(method, endpoint, http.StatusText(status)).Inc()
	reqLatency.WithLabelValues(endpoint).Observe(d.Seconds())
}

// SetPoints define o gauge de pontos para uma coleção/projeto.
func SetPoints(collection, project string, n int64) {
	pointsGauge.WithLabelValues(collection, project).Set(float64(n))
}

// SetUp marca o servidor como up (1) ou down (0).
func SetUp(up bool) {
	if up {
		upGauge.Set(1)
	} else {
		upGauge.Set(0)
	}
}
