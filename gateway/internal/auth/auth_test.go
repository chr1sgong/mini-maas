package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mini-maas/gateway/internal/openai"
)

func testStore() *StaticKeyStore {
	return NewStaticKeyStore(map[string]*Identity{
		"sk-good": {Key: "sk-good", Tenant: "tenant-a", Models: []string{"m1"}},
	})
}

func chatRequest(authHeader string, params *openai.InferenceParams) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if params != nil {
		req = req.WithContext(openai.WithParams(req.Context(), params))
	}
	return req
}

func TestAuthRejectsThreeStates(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected request reached next handler")
	})
	mw := NewMiddleware(testStore())(next)
	for name, header := range map[string]string{
		"no header":    "",
		"wrong prefix": "Token sk-good",
		"unknown key":  "Bearer sk-bad",
	} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, chatRequest(header, &openai.InferenceParams{Model: "m1"}))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
}

func TestAuthModelNotAllowedIs404(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected request reached next handler")
	})
	mw := NewMiddleware(testStore())(next)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, chatRequest("Bearer sk-good", &openai.InferenceParams{Model: "m2"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAuthInjectsIdentity(t *testing.T) {
	var got *Identity
	mw := NewMiddleware(testStore())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, chatRequest("Bearer sk-good", &openai.InferenceParams{Model: "m1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got == nil || got.Tenant != "tenant-a" {
		t.Fatalf("identity = %+v, want tenant-a", got)
	}
}

func TestHealthzExempt(t *testing.T) {
	mw := NewMiddleware(testStore())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
}
