package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodtune/whatsapp-mcp/internal/auth"
)

func TestGatewayWorkerBoundary(t *testing.T) {
	worker := httptest.NewServer(auth.RequireBearer("private", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/mcp" && r.URL.Path != "/internal/v1/media/uploads" && r.URL.Path != "/internal/v1/media/attachments" {
			t.Errorf("unexpected worker path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	})))
	defer worker.Close()

	gateway, err := newGateway(worker.URL, "public", "private")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ method, path string }{
		{"POST", "/mcp"}, {"POST", "/media/uploads"}, {"GET", "/media/attachments?message_id=a"},
	} {
		for _, token := range []string{"public", "private", ""} {
			req := httptest.NewRequest(item.method, item.path, strings.NewReader(""))
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			gateway.ServeHTTP(res, req)
			want := http.StatusUnauthorized
			if token == "public" {
				want = http.StatusAccepted
			}
			if res.Code != want {
				t.Errorf("%s token=%q status=%d want=%d", item.path, token, res.Code, want)
			}
		}
	}
	worker.Close()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer public")
	res := httptest.NewRecorder()
	gateway.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("worker stopped: %d", res.Code)
	}
}
