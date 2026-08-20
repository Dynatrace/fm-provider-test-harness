package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// This file holds the HTTP control plane under /__control__ to script CDN responses,
// inspect recorded requests, reset state and push SSE messages.

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
		Requests []cdnRequest `json:"requests"`
	}{Requests: s.state.recordedCdnRequests()}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, _ *http.Request) {
	resp := struct {
		Requests []metricsRequest `json:"requests"`
	}{Requests: s.state.recordedMetricsRequests()}
	writeJSON(w, http.StatusOK, resp)
}

// handleSSEClients reports how many SSE subscribers are currently connected
func (s *Server) handleSSEClients(w http.ResponseWriter, _ *http.Request) {
	resp := struct {
		Clients int `json:"clients"`
	}{Clients: s.state.sseClientCount()}
	writeJSON(w, http.StatusOK, resp)
}

// handleSSEDisconnect drops every connected SSE subscriber and gates new /sse connections according
// to the optional reconnectSeconds field:
//
//	omitted/null : refuse new connections (503) until /reset (or a later reconnectSeconds:0 call)
//	0            : accept new connections immediately, clearing any active outage (reconnect now)
//	> 0          : refuse new connections for that many seconds, then auto-recover
//	< 0          : rejected with 400
//
// The body is optional; an empty body means the omitted case. Only malformed JSON is an error.
func (s *Server) handleSSEDisconnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ReconnectSeconds *float64 `json:"reconnectSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var outage sseOutage
	switch {
	case body.ReconnectSeconds == nil:
		outage.forever = true
	case *body.ReconnectSeconds < 0:
		http.Error(w, "reconnectSeconds must not be negative", http.StatusBadRequest)
		return
	default:
		outage.window = time.Duration(*body.ReconnectSeconds * float64(time.Second))
	}
	s.state.disconnectSSEClients(outage)
	w.WriteHeader(http.StatusNoContent)
}

// handleSSEEmit broadcasts the posted JSON to every connected SSE subscriber verbatim.
// json.Compact validates the body is JSON and strips insignificant whitespace (incl.
// newlines) so the result stays a single line safe for `data: <payload>\n\n` framing.
func (s *Server) handleSSEEmit(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.state.broadcast(buf.String())
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
