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

// recordedRequest is a CDN request the backend observed since the last reset.
type recordedRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// state holds all mutable, per-scenario backend state. All access is guarded by mu.
type state struct {
	mu sync.Mutex

	// CDN response program. Each CDN GET consumes the next entry; once exhausted the last entry is
	// served repeatedly when repeatLast is true (otherwise a 404 is returned).
	responses  []cdnResponse
	repeatLast bool
	cursor     int

	// CDN requests observed since the last reset, in arrival order.
	requests []recordedRequest

	// Connected SSE subscribers. Each is a buffered channel of pre-formatted "data:" payloads.
	sseClients map[chan string]struct{}
}

func newState() *state {
	return &state{
		repeatLast: true,
		sseClients: make(map[chan string]struct{}),
	}
}

// reset clears the response program, recorded requests and cursor. SSE subscribers are left
// connected (a reset between scenarios must not drop a stream the provider just opened).
func (s *state) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = nil
	s.repeatLast = true
	s.cursor = 0
	s.requests = nil
}

// programResponses replaces the CDN response program and rewinds the cursor. Recorded requests are
// intentionally preserved so a step can program a new response and still count fetches across it.
func (s *state) programResponses(responses []cdnResponse, repeatLast bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = responses
	s.repeatLast = repeatLast
	s.cursor = 0
}

// nextResponse records the request and returns the CDN response to serve, or ok=false when the
// program is exhausted and repeatLast is disabled.
func (s *state) nextResponse(req recordedRequest) (cdnResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests = append(s.requests, req)

	if s.cursor < len(s.responses) {
		r := s.responses[s.cursor]
		s.cursor++
		return r, true
	}
	if s.repeatLast && len(s.responses) > 0 {
		return s.responses[len(s.responses)-1], true
	}
	return cdnResponse{}, false
}

func (s *state) recordedRequests() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
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
