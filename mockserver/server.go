package main

import (
	"log"
	"net/http"
)

// Server emulates every component the Dynatrace OpenFeature provider talks to (the CDN config
// endpoint and the metrics ingest endpoint) plus an SSE stream. It also exposes an HTTP control plane
// under /__control__ that the acceptance suites use to script responses and inspect requests.
//
// It is deliberately dependency-free (standard library only) so the published image stays tiny and
// every provider language (Java / Go / Python) drives the same contract.
//
// The handlers themselves live in:
//   - mock_api.go    — the provider-facing endpoints (CDN, metrics, SSE stream)
//   - control_api.go — the /__control__ plane the acceptance suites drive
type Server struct {
	state *state
}

func NewServer() *Server {
	return &Server{state: newState()}
}

// Handler returns the fully-wired HTTP handler. Method+path routing uses the Go 1.22 ServeMux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- provider-facing endpoints (see mock_api.go) ---
	mux.HandleFunc("GET /server/", s.handleCDN) // subtree: /server/{key}.json
	mux.HandleFunc("POST /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /sse", s.handleSSE)

	// --- control plane (see control_api.go) ---
	mux.HandleFunc("GET /__control__/health", s.handleHealth)
	mux.HandleFunc("POST /__control__/reset", s.handleReset)
	mux.HandleFunc("PUT /__control__/cdn/responses", s.handleProgramCDN)
	mux.HandleFunc("GET /__control__/cdn/requests", s.handleGetRequests)
	mux.HandleFunc("GET /__control__/metrics/requests", s.handleGetMetrics)
	mux.HandleFunc("POST /__control__/sse/emit", s.handleSSEEmit)

	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
