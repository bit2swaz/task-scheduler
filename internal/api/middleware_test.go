package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bit2swaz/task-scheduler/internal/api"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret"

// makeToken creates a signed HS256 JWT for test use.
func makeToken(nodeID, secret string, exp time.Time) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"node_id": nodeID,
		"exp":     exp.Unix(),
		"iat":     time.Now().Unix(),
	})
	str, _ := tok.SignedString([]byte(secret))
	return str
}

// protectedRouter wraps a single handler behind JWTMiddleware for isolation tests.
func protectedRouter(next http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Use(api.JWTMiddleware(testSecret))
	r.Get("/protected", next)
	return r
}

// T1: no Authorization header returns 401 UNAUTHORIZED.
func TestJWT_NoHeader(t *testing.T) {
	called := false
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.False(t, called)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "UNAUTHORIZED", body["code"])
}

// T2: malformed bearer token returns 401.
func TestJWT_Malformed(t *testing.T) {
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer this.is.not.valid")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// T3: expired token returns 401 with "token expired" in message.
func TestJWT_Expired(t *testing.T) {
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) {})
	tok := makeToken("node-1", testSecret, time.Now().Add(-time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Contains(t, body["message"], "token expired")
}

// T4: token signed with wrong secret returns 401.
func TestJWT_WrongSecret(t *testing.T) {
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) {})
	tok := makeToken("node-1", "wrong-secret", time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// T5: valid token passes through to handler.
func TestJWT_ValidToken(t *testing.T) {
	called := false
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	tok := makeToken("node-1", testSecret, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

// T6: JWT claims are injected into context; handler can extract node_id via ClaimsFromContext.
func TestJWT_ClaimsInContext(t *testing.T) {
	var gotNodeID string
	router := protectedRouter(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := api.ClaimsFromContext(r.Context())
		if ok {
			if id, ok2 := claims["node_id"].(string); ok2 {
				gotNodeID = id
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	tok := makeToken("node-42", testSecret, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "node-42", gotNodeID)
}

// T7: GET /health is public (no token needed); GET /tasks requires a valid token.
func TestJWT_PublicRoutesBypass(t *testing.T) {
	h, _, _ := newTestHandler()
	router := api.NewRouter(h)

	// public route - no auth required
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "GET /health should not require auth")

	// protected route - 401 without token
	req2 := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusUnauthorized, rr2.Code, "GET /tasks should require auth")
}

// T8: every response carries X-Request-Id set by the RequestID middleware.
func TestRequestID_Header(t *testing.T) {
	h, _, _ := newTestHandler()
	router := api.NewRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.NotEmpty(t, rr.Header().Get("X-Request-Id"))
}
