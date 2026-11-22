// Package config loads the server's runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

type Config struct {
	// DatabaseURL selects the database: a Postgres connection string, e.g.
	// postgres://user:pass@host:5432/whatsapp_mcp?sslmode=disable, or a
	// SQLite file, e.g. sqlite:store/whatsapp-mcp.db. Defaults to SQLite at
	// store/whatsapp-mcp.db when unset.
	DatabaseURL string
	BearerToken string
	HTTPAddr    string
	PublicURL   string
	MediaDir    string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: getEnvDefault("DATABASE_URL", "sqlite:store/whatsapp-mcp.db"),
		BearerToken: os.Getenv("MCP_BEARER_TOKEN"),
		HTTPAddr:    getEnvDefault("HTTP_ADDR", ":8080"),
		PublicURL:   strings.TrimSuffix(os.Getenv("PUBLIC_URL"), "/"),
		MediaDir:    getEnvDefault("MEDIA_DIR", "store/media"),
	}
	if !ValidToken(cfg.BearerToken) {
		return cfg, fmt.Errorf("MCP_BEARER_TOKEN must be a nonempty token without whitespace")
	}
	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ValidToken checks whether the configured token can fit in one bearer field.
func ValidToken(token string) bool {
	return token != "" && !strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
