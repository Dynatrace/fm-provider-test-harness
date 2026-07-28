package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// Server emulates every component the Dynatrace OpenFeature provider talks to (the CDN config
// endpoint and the metrics ingest endpoint) plus an SSE stream. It also exposes an HTTP control plane
// under /__control__ that the acceptance suites use to script responses and inspect requests.
//
// It is deliberately dependency-free (standard library only) so the published image stays tiny and
// every provider language (Java / Go / Python) drives the same contract.
type Server struct {
	state *state
}

func NewServer() *Server {
	return &Server{state: newState()}
}

// Handler returns the fully-wired HTTP handler. Method+path routing uses the Go 1.22 ServeMux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- provider-facing endpoints ---
	mux.HandleFunc("GET /server/", s.handleCDN) // subtree: /server/{key}.json
	mux.HandleFunc("POST /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /sse", s.handleSSE)

	// --- control plane ---
	mux.HandleFunc("GET /__control__/health", s.handleHealth)
	mux.HandleFunc("POST /__control__/reset", s.handleReset)
	mux.HandleFunc("PUT /__control__/cdn/responses", s.handleProgramCDN)
	mux.HandleFunc("GET /__control__/cdn/requests", s.handleGetRequests)
	mux.HandleFunc("POST /__control__/sse/emit", s.handleSSEEmit)

	return logging(mux)
}

// ---- provider-facing handlers ----------------------------------------------

func (s *Server) handleCDN(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.state.nextResponse(recordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: flattenHeaders(r.Header),
	})
	if !ok {
		http.Error(w, "no CDN response programmed", http.StatusNotFound) // TODO maybe 500
		return
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if resp.Body != nil {
		_, _ = w.Write([]byte(*resp.Body))
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	// Sink: the provider only needs a 2xx so metric flushes don't error. The http server closes and
	// drains the request body for us.
	// TODO store metrics in memory and expose them via the control plane so acceptance tests can assert on them
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan string, 16)
	s.state.addSSEClient(ch)
	defer s.state.removeSSEClient(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			if _, err := w.Write([]byte("data: " + payload + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---- control-plane handlers ------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.state.reset()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProgramCDN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Responses  []cdnResponse `json:"responses"`
		RepeatLast *bool         `json:"repeatLast"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	repeatLast := true
	if body.RepeatLast != nil {
		repeatLast = *body.RepeatLast
	}
	s.state.programResponses(body.Responses, repeatLast)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetRequests(w http.ResponseWriter, _ *http.Request) {
	resp := struct {
		Requests []recordedRequest `json:"requests"`
	}{Requests: s.state.recordedRequests()}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSSEEmit(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		Type         string `json:"type"`
		Etag         string `json:"etag,omitempty"`
		LastModified *int64 `json:"lastModified,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.state.broadcast(string(payload))
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ---------------------------------------------------------------

// flattenHeaders lower-cases header names and keeps the first value of each, matching the
// documented contract for GET /__control__/cdn/requests.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		if len(values) > 0 {
			out[strings.ToLower(name)] = values[0]
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
