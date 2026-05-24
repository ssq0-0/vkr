package metrics

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics содержит все метрики сервиса
type Metrics struct {
	requestsTotal       *prometheus.CounterVec
	errorsTotal         *prometheus.CounterVec
	framesProcessed     prometheus.Counter
	requestPayloadBytes *prometheus.CounterVec
	requestLatency      *prometheus.HistogramVec
	activeConnections   prometheus.Gauge
	processorPoolSize   prometheus.Gauge
}

// NewMetrics создает новый экземпляр метрик
func NewMetrics() *Metrics {
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_requests_total",
				Help: "Total number of requests by endpoint",
			},
			[]string{"endpoint"},
		),
		errorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_errors_total",
				Help: "Total number of errors by type",
			},
			[]string{"type"},
		),
		framesProcessed: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_frames_processed_total",
				Help: "Total number of frames processed",
			},
		),
		requestPayloadBytes: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_request_payload_bytes_total",
				Help: "Total decoded RGB payload bytes sent to the processor (by ingress)",
			},
			[]string{"endpoint"},
		),
		requestLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_request_latency_seconds",
				Help:    "Request latency by endpoint",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			},
			[]string{"endpoint"},
		),
		activeConnections: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "gateway_active_connections",
				Help: "Number of active WebSocket connections",
			},
		),
		processorPoolSize: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "gateway_processor_pool_size",
				Help: "Number of processors in the pool",
			},
		),
	}

	// Регистрируем метрики
	prometheus.MustRegister(
		m.requestsTotal,
		m.errorsTotal,
		m.framesProcessed,
		m.requestPayloadBytes,
		m.requestLatency,
		m.activeConnections,
		m.processorPoolSize,
	)

	return m
}

// IncrementRequests увеличивает счетчик запросов
func (m *Metrics) IncrementRequests(endpoint string) {
	m.requestsTotal.WithLabelValues(endpoint).Inc()
}

// IncrementErrors увеличивает счетчик ошибок
func (m *Metrics) IncrementErrors(errorType string) {
	m.errorsTotal.WithLabelValues(errorType).Inc()
}

// IncrementProcessedFrames увеличивает счетчик обработанных кадров
func (m *Metrics) IncrementProcessedFrames() {
	m.framesProcessed.Inc()
}

// AddRequestPayloadBytes добавляет объём принятого полезного груза (сырые RGB байты после Base64).
func (m *Metrics) AddRequestPayloadBytes(endpoint string, n int) {
	if n <= 0 {
		return
	}
	m.requestPayloadBytes.WithLabelValues(endpoint).Add(float64(n))
}

// RecordLatency записывает latency запроса
func (m *Metrics) RecordLatency(endpoint string, duration time.Duration) {
	m.requestLatency.WithLabelValues(endpoint).Observe(duration.Seconds())
}

// SetActiveConnections устанавливает количество активных соединений
func (m *Metrics) SetActiveConnections(count int) {
	m.activeConnections.Set(float64(count))
}

// SetProcessorPoolSize устанавливает размер пула процессоров
func (m *Metrics) SetProcessorPoolSize(size int) {
	m.processorPoolSize.Set(float64(size))
}

// Handler возвращает HTTP handler для метрик Prometheus
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}

// RateLimiter простой rate limiter
type RateLimiter struct {
	tokens chan struct{}
}

// NewRateLimiter создает новый rate limiter
func NewRateLimiter(rps int) *RateLimiter {
	rl := &RateLimiter{
		tokens: make(chan struct{}, rps),
	}

	// Заполняем токенами
	for i := 0; i < rps; i++ {
		rl.tokens <- struct{}{}
	}

	// Пополняем токены
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()

		for range ticker.C {
			select {
			case rl.tokens <- struct{}{}:
			default:
			}
		}
	}()

	return rl
}

// Allow проверяет, разрешен ли запрос
func (rl *RateLimiter) Allow() bool {
	select {
	case <-rl.tokens:
		return true
	default:
		return false
	}
}

// Middleware возвращает HTTP middleware для rate limiting
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/admin/") || p == "/metrics" || p == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
