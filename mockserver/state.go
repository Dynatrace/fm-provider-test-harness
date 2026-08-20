package main

import (
	"sync"
	"time"
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

	// Connected SSE subscribers. The key is the buffered channel of pre-formatted "data:" payloads;
	// the value is a per-client channel closed to force the subscriber's handler to disconnect.
	sseClients map[chan string]chan struct{}

	// SSE outage gate (see disconnectSSEClients). New /sse connections are refused while
	// sseClosedForever is set, or while now is before sseClosedUntil. Both zero => SSE is available.
	sseClosedUntil   time.Time
	sseClosedForever bool
}

func newState() *state {
	return &state{
		repeatLast: true,
		sseClients: make(map[chan string]chan struct{}),
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
	s.sseClosedUntil = time.Time{}
	s.sseClosedForever = false
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

// addSSEClient registers a subscriber and returns a channel that is closed when the client should
// disconnect (either via disconnectSSEClients or normal teardown from removeSSEClient).
func (s *state) addSSEClient(ch chan string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan struct{})
	s.sseClients[ch] = done
	return done
}

func (s *state) removeSSEClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sseClients, ch)
}

// sseClientCount returns the number of SSE subscribers currently connected.
func (s *state) sseClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sseClients)
}

// sseOutage describes what disconnectSSEClients should do to the /sse gate after dropping clients.
type sseOutage struct {
	// forever refuses new /sse connections until reset (or a subsequent clearing disconnect).
	forever bool
	// window, when > 0, refuses new /sse connections for that duration and then auto-recovers.
	// A zero window (and forever == false) clears any active outage, accepting connections again.
	window time.Duration
}

// disconnectSSEClients drops every connected SSE subscriber by closing each one's done channel and
// applies the requested outage to the /sse gate. This lets suites exercise the provider's
// reconnect/backoff behaviour: an indefinite outage (until reset), a fixed window, or an immediate
// reconnect (a zero window, which also clears a previously-set outage).
func (s *state) disconnectSSEClients(o sseOutage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case o.forever:
		s.sseClosedForever = true
		s.sseClosedUntil = time.Time{}
	case o.window > 0:
		s.sseClosedForever = false
		s.sseClosedUntil = time.Now().Add(o.window)
	default: // zero window => clear any outage and accept new connections immediately
		s.sseClosedForever = false
		s.sseClosedUntil = time.Time{}
	}

	for ch, done := range s.sseClients {
		close(done)
		delete(s.sseClients, ch)
	}
}

// sseAvailable reports whether new SSE connections are currently accepted. It returns false during
// an outage opened by disconnectSSEClients — indefinitely, or until the outage window elapses.
func (s *state) sseAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sseClosedForever {
		return false
	}
	return !time.Now().Before(s.sseClosedUntil)
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
