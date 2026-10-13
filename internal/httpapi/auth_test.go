package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAuthOpenWithoutToken(t *testing.T) {
	h := New(nil, nil) // AuthToken vazio
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	for _, path := range []string{"/mcp", "/search", "/status", "/dashboard", "/health"} {
		rec := httptest.NewRecorder()
		RequireAuth(h, inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Errorf("sem token configurado, %s deveria passar, veio %d", path, rec.Code)
		}
	}
}

func TestRequireAuthDeniesWithoutHeader(t *testing.T) {
	h := New(nil, nil)
	h.AuthToken = "segredo"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	for _, path := range []string{"/mcp", "/search", "/status", "/dashboard"} {
		rec := httptest.NewRecorder()
		RequireAuth(h, inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s sem header deveria dar 401, veio %d", path, rec.Code)
		}
	}
}

func TestRequireAuthDeniesWrongToken(t *testing.T) {
	h := New(nil, nil)
	h.AuthToken = "segredo"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer errado")
	rec := httptest.NewRecorder()
	RequireAuth(h, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token errado deveria dar 401, veio %d", rec.Code)
	}
}

func TestRequireAuthAllowsCorrectTokenAndKeepsHealthOpen(t *testing.T) {
	h := New(nil, nil)
	h.AuthToken = "segredo"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer segredo")
	rec := httptest.NewRecorder()
	RequireAuth(h, inner).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("token certo deveria passar, veio %d", rec.Code)
	}
	// /health nunca exige auth (liveness).
	rec2 := httptest.NewRecorder()
	RequireAuth(h, inner).ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec2.Code != 200 {
		t.Fatalf("/health deveria passar sem token, veio %d", rec2.Code)
	}
}
