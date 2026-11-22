// Command whatsapp-mcp serves one linked WhatsApp account over MCP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/goodtune/whatsapp-mcp/internal/auth"
	"github.com/goodtune/whatsapp-mcp/internal/config"
	"github.com/goodtune/whatsapp-mcp/internal/httpapi"
)

// version is injected at build time via -ldflags "-X main.version=...". It is
// the v-stripped semantic version (0.2.0): GoReleaser's {{.Version}} strips
// the tag's leading "v".
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	workerRole := fs.Bool("worker", false, "Run the private WhatsApp worker instead of the MCP gateway.")
	showVersion := fs.Bool("version", false, "Print the version and exit.")
	fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	log.Printf("whatsapp-mcp %s", version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerToken := os.Getenv("WORKER_BEARER_TOKEN")
	if !config.ValidToken(workerToken) {
		return fmt.Errorf("WORKER_BEARER_TOKEN must be a nonempty token without whitespace")
	}
	if *workerRole {
		return runWorker(ctx, workerToken)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	address := os.Getenv("WORKER_URL")
	if address == "" {
		address = "http://127.0.0.1:8081"
	}
	handler, err := newGateway(address, cfg.BearerToken, workerToken)
	if err != nil {
		return err
	}
	listener, publicURL, err := listen(cfg)
	if err != nil {
		return err
	}
	log.Printf("MCP endpoint: %s/mcp", publicURL)
	return serve(ctx, listener, handler)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func newHTTPHandler(server *mcp.Server, bearerToken string) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
	mux := http.NewServeMux()
	httpapi.Register(mux)
	mux.Handle("/mcp", auth.RequireBearer(bearerToken, mcpHandler))
	return mux
}

func listen(cfg config.Config) (net.Listener, string, error) {
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to listen on %s: %w", cfg.HTTPAddr, err)
	}
	log.Printf("whatsapp-mcp listening on %s", cfg.HTTPAddr)
	publicURL := cfg.PublicURL
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	}
	return listener, publicURL, nil
}
