package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPRoutes(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	handler := newHTTPHandler(server, "test-token")
	for path, status := range map[string]int{
		"/healthz":                              http.StatusOK,
		"/mcp":                                  http.StatusUnauthorized,
		"/.well-known/oauth-protected-resource": http.StatusNotFound,
		"/.well-known/oauth-authorization-server": http.StatusNotFound,
		"/oauth/authorize":                        http.StatusNotFound,
		"/oauth/github/callback":                  http.StatusNotFound,
		"/oauth/token":                            http.StatusNotFound,
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != status {
			t.Errorf("%s status = %d, want %d", path, res.Code, status)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
	))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "serverInfo") {
		t.Fatalf("MCP initialize: %d %s", res.Code, res.Body.String())
	}
}
