package main

import (
	"sync"
)

// cdnResponse is a single programmed CDN response.
//
// Body is a pointer so an explicitly-empty body ("body": "") can be distinguished from an absent
// one (field omitted) — the former is served as a zero-length body, the latter as no body at all.
type cdnResponse struct {
	Status  int               `json:"status"`
	Body    *string           `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// cdnRequest is a CDN request the backend observed since the last reset.
type cdnRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// metricsRequest is a metrics-ingest request the backend observed since the last reset.
//
// Unlike CDN requests, the body is the payload under test, so it is captured verbatim (as a string
// so acceptance suites can assert on the raw bytes regardless of encoding).
type metricsRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// state holds all mutable, per-scenario backend state. All access is guarded by mu.
type state struct {
	mu sync.Mutex

	// CDN response program. Each CDN GET consumes the next entry; once exhausted the last entry is
	// served repeatedly when repeatLast is true (otherwise a 404 is returned).
	cdnResponses []cdnResponse
	repeatLast   bool
	cursor       int

	// CDN cdnRequests observed since the last reset, in arrival order.
	cdnRequests []cdnRequest

	// Metrics-ingest cdnRequests observed since the last reset, in arrival order.
	metricsRequests []metricsRequest

	// Connected SSE subscribers. Each is a buffered channel of pre-formatted "data:" payloads.
	sseClients map[chan string]struct{}
}

func newState() *state {
	return &state{
		repeatLast: true,
		sseClients: make(map[chan string]struct{}),
	}
}

func (s *state) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cdnResponses = nil
	s.repeatLast = true
	s.cursor = 0
	s.cdnRequests = nil
	s.metricsRequests = nil
}

// programResponses replaces the CDN response program and rewinds the cursor. Recorded cdnRequests are
// intentionally preserved so a step can program a new response and still count fetches across it.
func (s *state) programResponses(responses []cdnResponse, repeatLast bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cdnResponses = responses
	s.repeatLast = repeatLast
	s.cursor = 0
}

// nextResponse records the request and returns the CDN response to serve, or ok=false when the
// program is exhausted and repeatLast is disabled.
func (s *state) nextResponse(req cdnRequest) (cdnResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cdnRequests = append(s.cdnRequests, req)

	if s.cursor < len(s.cdnResponses) {
		r := s.cdnResponses[s.cursor]
		s.cursor++
		return r, true
	}
	if s.repeatLast && len(s.cdnResponses) > 0 {
		return s.cdnResponses[len(s.cdnResponses)-1], true
	}
	return cdnResponse{}, false
}

func (s *state) recordedCdnRequests() []cdnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]cdnRequest, len(s.cdnRequests))
	copy(out, s.cdnRequests)
	return out
}

// recordMetrics appends a metrics-ingest request to the log. Unlike CDN cdnRequests there is no
// programmed response to consume — the endpoint is a pure sink — so this only records.
func (s *state) recordMetrics(req metricsRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metricsRequests = append(s.metricsRequests, req)
}

func (s *state) recordedMetricsRequests() []metricsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metricsRequest, len(s.metricsRequests))
	copy(out, s.metricsRequests)
	return out
}

func (s *state) addSSEClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sseClients[ch] = struct{}{}
}

func (s *state) removeSSEClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sseClients, ch)
}

// broadcast delivers payload to every connected SSE subscriber without blocking on slow clients.
func (s *state) broadcast(payload string) {
	s.mu.Lock()
	clients := make([]chan string, 0, len(s.sseClients))
	for ch := range s.sseClients {
		clients = append(clients, ch)
	}
	s.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- payload:
		default: // drop for a subscriber that isn't keeping up rather than blocking the control call
		}
	}
}
