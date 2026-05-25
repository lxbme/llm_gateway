package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"llm_gateway/internal/metrics"

	"golang.org/x/time/rate"
)

const tokenPrefix = "sk"
const tokenEntropyLen = 32

const tokenGenSpeed = 100
const tokenCapacity = 200
const parallelCount = 50

type Middleware func(http.Handler) http.Handler

var (
	rateLimiter       = rate.NewLimiter(rate.Limit(tokenGenSpeed), tokenCapacity)
	parallelSemaphore = make(chan struct{}, parallelCount)
)

func chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// WithMetricsMiddleware wraps h with the Prometheus instrumentation middleware.
// Exported so cmd/gateway/main.go can apply it without touching the private chain helper.
func WithMetricsMiddleware(h http.Handler) http.Handler {
	return metricsMiddleware(h)
}

// metricsMiddleware records HTTPInFlight, HTTPRequestsTotal, HTTPDurationSec
// for every public HTTP request. It uses r.URL.Path verbatim as the `path`
// label — today the gateway only registers /v1/chat/completions, so there is
// no high-cardinality risk. Add a whitelist here if new public paths land.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metrics.HTTPInFlight.Inc()
		defer metrics.HTTPInFlight.Dec()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			path := r.URL.Path
			metrics.HTTPRequestsTotal.WithLabelValues(path, strconv.Itoa(rec.status)).Inc()
			metrics.HTTPDurationSec.WithLabelValues(path).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

// statusRecorder wraps ResponseWriter so the middleware can read back the
// status code after the handler returns. http.Flusher is forwarded explicitly
// because gateway handlers stream SSE responses.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteStatus bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteStatus {
		s.status = code
		s.wroteStatus = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteStatus {
		s.wroteStatus = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func adminCheckMiddleware(next http.Handler) http.Handler {
	secret := os.Getenv("ADMIN_SECRET")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Secret") != secret || secret == "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bindJSON(r *http.Request, obj interface{}) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(obj)
	if err != nil {
		return fmt.Errorf("json decode error: %w", err)
	}

	return nil
}
