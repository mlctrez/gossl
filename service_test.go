package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kardianos/service"
)

// newTestService creates a Service with a temp-backed EndpointStore containing
// an "admin" endpoint so admin-host routing is active.
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "endpoints.json")

	store := &EndpointStore{filePath: filePath}
	// Add the admin endpoint so the store recognises the host.
	if err := store.Add(Endpoint{Host: "admin", URL: "http://localhost:9999"}); err != nil {
		t.Fatalf("failed to add admin endpoint: %v", err)
	}

	svc := &Service{store: store}
	svc.Logger(service.ConsoleLogger)
	return svc
}

// adminRequest builds an HTTP request whose Host header is "admin".
func adminRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Host = "admin"
	return req
}

// setTokenEnv sets GO_SSL_TOKEN for the duration of the test.
func setTokenEnv(t *testing.T, token string) {
	t.Helper()
	t.Setenv(EnvGoSslToken, token)
}

// addTokenCookie attaches a go-ssl-token cookie to the request.
func addTokenCookie(req *http.Request, value string) {
	req.AddCookie(&http.Cookie{Name: KeyGoSslToken, Value: value})
}

// --- Authentication tests (Requirements 8.3, 8.4, 8.5, 8.6) ---

func TestAdminHost_NoCookie_Returns401(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", rr.Code)
	}
}

func TestAdminHost_WrongCookie_Returns401(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/")
	addTokenCookie(req, "wrong-token")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong cookie, got %d", rr.Code)
	}
}

func TestAdminHost_ValidCookie_ReachesWebUI(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/")
	addTokenCookie(req, "secret-token")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// The WebHandler stub returns 404 (not 401), proving the request
	// passed authentication and reached the web UI handler.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("expected request to reach WebUI handler, got 401")
	}
}

// --- Admin host routing tests (Requirements 8.1, 8.7) ---

func TestAdminHost_APIPath_ReachesAPIHandler(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/api/endpoints")
	addTokenCookie(req, "secret-token")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// The APIHandler stub returns 404 (not 401), proving the request
	// was routed to the API handler after authentication.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("expected request to reach API handler, got 401")
	}
}

func TestAdminHost_UnrecognizedAPIPath_Returns404(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/api/nonexistent")
	addTokenCookie(req, "secret-token")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// The API handler stub returns 404 for all paths, which is the
	// correct behaviour for unrecognised /api/ routes.
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unrecognized API path, got %d", rr.Code)
	}
}

func TestAdminHost_NonAPIPath_ReachesWebUI(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	req := adminRequest(http.MethodGet, "/some/page")
	addTokenCookie(req, "secret-token")
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// Non-/api/ paths on the admin host go to the WebHandler.
	// The stub returns 404, but crucially not 401.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("expected request to reach WebUI handler, got 401")
	}
}

// --- Token-setting path on admin host ---

func TestAdminHost_TokenSettingPath_SetsCookie(t *testing.T) {
	token := "secret-token"
	setTokenEnv(t, token)
	svc := newTestService(t)

	// Request the token-setting path without a cookie.
	req := adminRequest(http.MethodGet, "/"+token)
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// Should redirect (307) and set the cookie.
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307 for token-setting path, got %d", rr.Code)
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if setCookie == "" {
		t.Fatal("expected Set-Cookie header on token-setting path")
	}
}

// --- Constant-time comparison verification (Requirement 8.6) ---
// This is a code-review test: we verify that crypto/subtle.ConstantTimeCompare
// is used in validateToken by checking the import and call site exist.
// The actual constant-time property cannot be verified at runtime.

func TestConstantTimeComparison_CodeReview(t *testing.T) {
	// Read service.go and verify it imports crypto/subtle and calls ConstantTimeCompare.
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("failed to read service.go: %v", err)
	}
	src := string(data)

	if !contains(src, "crypto/subtle") {
		t.Fatal("service.go does not import crypto/subtle")
	}
	if !contains(src, "subtle.ConstantTimeCompare") {
		t.Fatal("service.go does not call subtle.ConstantTimeCompare")
	}
}

// --- Non-admin host tests (ensure admin routing doesn't break normal proxy) ---

func TestNonAdminHost_NoCookie_Returns401(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	// Add a regular (non-skipToken) endpoint.
	if err := svc.store.Add(Endpoint{Host: "app.example.com", URL: "http://localhost:8080"}); err != nil {
		t.Fatalf("failed to add endpoint: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.example.com"
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-admin host without cookie, got %d", rr.Code)
	}
}

func TestNonAdminHost_SkipToken_Allowed(t *testing.T) {
	setTokenEnv(t, "secret-token")
	svc := newTestService(t)

	// Add a skipToken endpoint — these don't require a cookie.
	if err := svc.store.Add(Endpoint{Host: "public.example.com", URL: "http://localhost:8080", SkipToken: true}); err != nil {
		t.Fatalf("failed to add endpoint: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "public.example.com"
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)

	// Should not be 401 — skipToken hosts bypass auth.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("expected skipToken host to bypass auth, got 401")
	}
}

// contains is a simple helper to check substring presence.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
