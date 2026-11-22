package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/goodtune/whatsapp-mcp/internal/auth"
	"github.com/goodtune/whatsapp-mcp/internal/db"
	"github.com/goodtune/whatsapp-mcp/internal/httpapi"
	"github.com/goodtune/whatsapp-mcp/internal/mcpserver"
	"github.com/goodtune/whatsapp-mcp/internal/store"
	"github.com/goodtune/whatsapp-mcp/internal/wa"
)

const workerPrefix = "/internal/v1"

func newGateway(address, publicToken, workerToken string) (http.Handler, error) {
	target, err := url.Parse(address)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, fmt.Errorf("WORKER_URL must be an HTTP origin")
	}
	if publicToken == workerToken {
		return nil, fmt.Errorf("WORKER_BEARER_TOKEN must differ from MCP_BEARER_TOKEN")
	}
	target.Path = workerPrefix
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 2 * time.Minute
	transport.DisableKeepAlives = true // Avoid replaying calls on reused stale connections.
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			r.Out.Header.Set("Authorization", "Bearer "+workerToken)
			r.Out.Header.Del("Idempotency-Key")
			r.Out.Header.Del("X-Idempotency-Key")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("worker request failed: %v", err)
			http.Error(w, "WhatsApp worker unavailable; send delivery may be uncertain", http.StatusServiceUnavailable)
		},
	}
	mux := http.NewServeMux()
	httpapi.Register(mux)
	protected := auth.RequireBearer(publicToken, proxy)
	mux.Handle("/mcp", protected)
	mux.Handle("POST /media/uploads", protected)
	mux.Handle("GET /media/attachments", protected)
	return mux, nil
}

func runWorker(ctx context.Context, token string) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "sqlite:store/whatsapp-mcp.db"
	}
	session, dialect, err := db.Open(dsn)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := db.Migrate(ctx, session, dialect); err != nil {
		return err
	}
	memory, err := db.OpenVolatileHistory(ctx)
	if err != nil {
		return err
	}
	defer memory.Close()
	history := store.New(memory)
	manager, err := wa.NewManager(ctx, session, dialect, history, "")
	if err != nil {
		return err
	}
	if err := manager.EnableFeed(ctx); err != nil {
		return err
	}

	server := mcpserver.NewWorker(manager, history)
	defer server.CloseUploads()
	tools := newHTTPHandler(server.MCPServer(), token)
	media := http.NewServeMux()
	server.RegisterMedia(media)
	api := http.NewServeMux()
	api.Handle("/mcp", auth.RequireBearer(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := manager.PruneFeed(r.Context()); err != nil {
			http.Error(w, "cannot prune expired updates", 503)
			return
		}
		tools.ServeHTTP(w, r)
	})))
	api.Handle("/media/", auth.RequireBearer(token, media))

	mux := http.NewServeMux()
	httpapi.Register(mux)
	mux.Handle(workerPrefix+"/", http.StripPrefix(workerPrefix, api))
	address := os.Getenv("WORKER_ADDR")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	go func() {
		if err := manager.Reconnect(ctx); err != nil {
			log.Printf("warning: failed to reconnect the linked WhatsApp device: %v", err)
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				server.Maintain()
				if err := manager.PruneFeed(ctx); err != nil {
					log.Printf("prune updates: %v", err)
				}
			}
		}
	}()
	return serve(ctx, listener, mux)
}
