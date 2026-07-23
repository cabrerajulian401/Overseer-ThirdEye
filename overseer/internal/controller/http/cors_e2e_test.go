package http

import (
	"net/http"
	"testing"
)

// A browser preflights cross-origin requests with OPTIONS before sending the
// real call; without CORS headers in the response it blocks the request
// entirely, so the frontend can't talk to the API at all.
func TestCORSPreflightAllowsFrontendOrigin(t *testing.T) {
	e := newEnv(t)
	defer e.srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, e.srv.URL+"/orders", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "Authorization")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", res.StatusCode, http.StatusNoContent)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Access-Control-Allow-Headers missing: browser would strip Authorization")
	}
}

// Simple (non-preflighted) requests still need the allow-origin header on
// the actual response for the browser to hand the body to frontend JS.
func TestCORSActualResponseCarriesAllowOrigin(t *testing.T) {
	e := newEnv(t)
	defer e.srv.Close()

	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/orders", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Authorization", "Bearer test-token")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}
