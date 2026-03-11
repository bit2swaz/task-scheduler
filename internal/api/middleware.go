// Package api provides HTTP middleware: JWT authentication, request ID
// injection, structured logging, and panic recovery.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// claimsContextKey is an unexported type for context keys in this package.
type claimsContextKey struct{}

// JWTMiddleware returns a chi-compatible middleware that validates a
// Bearer token in the Authorization header against the provided HMAC secret.
// Claims are stored in the request context for downstream handlers.
func JWTMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeError(w, http.StatusUnauthorized, codeUnauth, "missing or invalid authorization header")
				return
			}
			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(secret), nil
			})
			if err != nil {
				msg := "invalid token"
				if errors.Is(err, jwt.ErrTokenExpired) {
					msg = "token expired"
				}
				writeError(w, http.StatusUnauthorized, codeUnauth, msg)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok || !token.Valid {
				writeError(w, http.StatusUnauthorized, codeUnauth, "invalid token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext returns the JWT MapClaims stored in the request context.
// Returns false if no claims are present (i.e., request did not pass through JWTMiddleware).
func ClaimsFromContext(ctx context.Context) (jwt.MapClaims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(jwt.MapClaims)
	return claims, ok
}
