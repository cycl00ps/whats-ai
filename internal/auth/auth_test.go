package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireBearer(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/mcp", RequireBearer("test-secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, header := range []string{"", "Bearer wrong", "Bearer test-secret trailing", "Basic test-secret", "Bearer test-secret"} {
			req := httptest.NewRequest(method, "/mcp", nil)
			req.Header.Set("Mcp-Session-Id", "existing-session")
			req.Header.Set("Authorization", header)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			want := http.StatusUnauthorized
			if header == "Bearer test-secret" {
				want = http.StatusNoContent
			}
			if res.Code != want || want == http.StatusUnauthorized && res.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s with %q: got status %d, want %d", method, header, res.Code, want)
			}
		}
	}
}
