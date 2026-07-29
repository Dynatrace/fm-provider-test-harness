package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// do executes a request against the server handler using an in-memory recorder (no sockets).
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHealth(t *testing.T) {
	h := NewServer().Handler()
	rec := do(t, h, "GET", "/__control__/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
}

func TestProgramAndServeCDN(t *testing.T) {
	s := NewServer()
	h := s.Handler()

	program(t, h, `{"responses":[{"status":200,"body":"{\"flags\":{}}","headers":{"ETag":"\"v1\""}}]}`)

	rec := do(t, h, "GET", "/server/dt01.server_us_key.json", "")
	if rec.Code != 200 {
		t.Fatalf("cdn status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"flags":{}}` {
		t.Fatalf("cdn body = %q", got)
	}
	if got := rec.Header().Get("ETag"); got != `"v1"` {
		t.Fatalf("ETag = %q, want \"v1\"", got)
	}
}

func TestStickyLastResponse(t *testing.T) {
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":200,"body":"first"}],"repeatLast":true}`)

	for i := 0; i < 3; i++ {
		rec := do(t, h, "GET", "/server/x.json", "")
		if rec.Body.String() != "first" {
			t.Fatalf("fetch %d body = %q, want sticky \"first\"", i, rec.Body.String())
		}
	}
}

func TestRetrySequenceConsumesQueue(t *testing.T) {
	// Models "500 then 200 on retry": two fetches consume both entries in order, then stick on 200.
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":500},{"status":200,"body":"ok"}],"repeatLast":true}`)

	if rec := do(t, h, "GET", "/server/x.json", ""); rec.Code != 500 {
		t.Fatalf("first fetch = %d, want 500", rec.Code)
	}
	if rec := do(t, h, "GET", "/server/x.json", ""); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("second fetch = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, "GET", "/server/x.json", ""); rec.Code != 200 {
		t.Fatalf("third fetch = %d, want sticky 200", rec.Code)
	}
}

func TestEmptyVersusAbsentBody(t *testing.T) {
	s := NewServer()
	h := s.Handler()

	program(t, h, `{"responses":[{"status":200,"body":""}]}`)
	rec := do(t, h, "GET", "/server/x.json", "")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("empty-body response = %d len %d, want 200 len 0", rec.Code, rec.Body.Len())
	}

	program(t, h, `{"responses":[{"status":500}]}`)
	rec = do(t, h, "GET", "/server/x.json", "")
	if rec.Code != 500 || rec.Body.Len() != 0 {
		t.Fatalf("no-body response = %d len %d, want 500 len 0", rec.Code, rec.Body.Len())
	}
}

func TestRetryAfterHeaderPassthrough(t *testing.T) {
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":429,"headers":{"Retry-After":"60"}}]}`)
	rec := do(t, h, "GET", "/server/x.json", "")
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("429 = %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestRecordsRequestsWithLowercasedHeaders(t *testing.T) {
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":304}]}`)

	r := httptest.NewRequest("GET", "/server/dt01.server_us_key.json", nil)
	r.Header.Set("If-None-Match", `"v1"`)
	r.Header.Set("If-Modified-Since", "Mon, 01 Jan 2024 00:00:00 GMT")
	h.ServeHTTP(httptest.NewRecorder(), r)

	reqs := getRequests(t, h)
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Path != "/server/dt01.server_us_key.json" {
		t.Fatalf("recorded path = %q", got.Path)
	}
	if got.Headers["if-none-match"] != `"v1"` {
		t.Fatalf("if-none-match = %q", got.Headers["if-none-match"])
	}
	if got.Headers["if-modified-since"] == "" {
		t.Fatalf("if-modified-since not recorded (lower-cased)")
	}
}

func TestInitialFetchHasNoConditionalHeaders(t *testing.T) {
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":200,"body":"{}"}]}`)

	do(t, h, "GET", "/server/x.json", "")

	reqs := getRequests(t, h)
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	if _, ok := reqs[0].Headers["if-none-match"]; ok {
		t.Fatal("initial fetch unexpectedly carried if-none-match")
	}
	if _, ok := reqs[0].Headers["if-modified-since"]; ok {
		t.Fatal("initial fetch unexpectedly carried if-modified-since")
	}
}

func TestResetClearsResponsesAndRequests(t *testing.T) {
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":200,"body":"x"}]}`)
	do(t, h, "GET", "/server/x.json", "")

	if rec := do(t, h, "POST", "/__control__/reset", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reset = %d, want 204", rec.Code)
	}

	if reqs := getRequests(t, h); len(reqs) != 0 {
		t.Fatalf("after reset recorded %d requests, want 0", len(reqs))
	}
	// With no program and repeatLast defaulted, a fetch now 404s.
	if rec := do(t, h, "GET", "/server/x.json", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("after reset fetch = %d, want 404", rec.Code)
	}
}

func TestProgramPreservesRecordedRequests(t *testing.T) {
	// Re-programming mid-scenario must not wipe the request log (steps count fetches across it).
	s := NewServer()
	h := s.Handler()
	program(t, h, `{"responses":[{"status":200,"body":"a"}]}`)
	do(t, h, "GET", "/server/x.json", "")
	program(t, h, `{"responses":[{"status":304}]}`)

	if reqs := getRequests(t, h); len(reqs) != 1 {
		t.Fatalf("after re-program recorded %d requests, want 1 preserved", len(reqs))
	}
}

func TestMetricsSinkReturns202(t *testing.T) {
	h := NewServer().Handler()
	rec := do(t, h, "POST", "/v1/metrics", `{"metrics":[]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("metrics = %d, want 202", rec.Code)
	}
}

func TestMetricsRequestsAreRecorded(t *testing.T) {
	h := NewServer().Handler()

	r := httptest.NewRequest("POST", "/v1/metrics", strings.NewReader(`{"metrics":[{"key":"flag.a"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Api-Token dt0c01.abc")
	h.ServeHTTP(httptest.NewRecorder(), r)

	reqs := getMetricsRequests(t, h)
	if len(reqs) != 1 {
		t.Fatalf("recorded %d metrics requests, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != "POST" || got.Path != "/v1/metrics" {
		t.Fatalf("recorded %s %s, want POST /v1/metrics", got.Method, got.Path)
	}
	if got.Body != `{"metrics":[{"key":"flag.a"}]}` {
		t.Fatalf("recorded body = %q", got.Body)
	}
	if got.Headers["content-type"] != "application/json" {
		t.Fatalf("content-type = %q (want lower-cased key)", got.Headers["content-type"])
	}
	if got.Headers["authorization"] != "Api-Token dt0c01.abc" {
		t.Fatalf("authorization = %q", got.Headers["authorization"])
	}
}

func TestResetClearsMetricsRequests(t *testing.T) {
	h := NewServer().Handler()
	do(t, h, "POST", "/v1/metrics", `{"metrics":[]}`)

	if rec := do(t, h, "POST", "/__control__/reset", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reset = %d, want 204", rec.Code)
	}
	if reqs := getMetricsRequests(t, h); len(reqs) != 0 {
		t.Fatalf("after reset recorded %d metrics requests, want 0", len(reqs))
	}
}

func TestSSEEmitBroadcasts(t *testing.T) {
	s := NewServer()
	h := s.Handler()

	ch := make(chan string, 1)
	s.state.addSSEClient(ch)
	defer s.state.removeSSEClient(ch)

	rec := do(t, h, "POST", "/__control__/sse/emit", `{"type":"refetchConfig","lastModified":1735776000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sse emit = %d, want 204", rec.Code)
	}

	select {
	case payload := <-ch:
		var msg map[string]any
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		if msg["type"] != "refetchConfig" {
			t.Fatalf("payload type = %v, want refetchConfig", msg["type"])
		}
	default:
		t.Fatal("no SSE payload broadcast to subscriber")
	}
}

// ---- helpers ----

func program(t *testing.T, h http.Handler, body string) {
	t.Helper()
	rec := do(t, h, "PUT", "/__control__/cdn/responses", body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("program responses = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
}

func getRequests(t *testing.T, h http.Handler) []cdnRequest {
	t.Helper()
	rec := do(t, h, "GET", "/__control__/cdn/requests", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get requests = %d, want 200", rec.Code)
	}
	var resp struct {
		Requests []cdnRequest `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	return resp.Requests
}

func getMetricsRequests(t *testing.T, h http.Handler) []metricsRequest {
	t.Helper()
	rec := do(t, h, "GET", "/__control__/metrics/requests", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get metrics requests = %d, want 200", rec.Code)
	}
	var resp struct {
		Requests []metricsRequest `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode metrics requests: %v", err)
	}
	return resp.Requests
}
