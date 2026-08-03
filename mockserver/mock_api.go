package main

import (
	"io"
	"net/http"
	"strings"
)

// This file holds the provider-facing (mocked) endpoints: the CDN config endpoint, the metrics
// ingest endpoint and the SSE stream. These emulate the real Dynatrace backend the provider talks to.

func (s *Server) handleCDN(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.state.nextResponse(cdnRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: flattenHeaders(r.Header),
	})
	if !ok {
		http.Error(w, "no CDN response programmed", http.StatusInternalServerError)
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

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.state.recordMetrics(metricsRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: flattenHeaders(r.Header),
		Body:    string(body),
	})
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
