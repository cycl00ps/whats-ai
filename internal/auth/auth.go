package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// RequireBearer protects every request to the wrapped endpoint.
func RequireBearer(token string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if token != "" && len(r.Header.Values("Authorization")) == 1 && len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			actual := sha256.Sum256([]byte(fields[1]))
			if subtle.ConstantTimeCompare(expected[:], actual[:]) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}
