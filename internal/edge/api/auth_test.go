package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAuthenticate(t *testing.T) {
	secret := []byte(strings.Repeat("x", 32))
	handler := authenticate(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := currentClaims(r.Context())
		if claims.Subject != "user-1" || claims.Role != "buyer" {
			t.Errorf("unexpected claims: %+v", claims)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	token := signedToken(t, secret, "buyer", time.Now().Add(time.Minute))

	request := httptest.NewRequest(http.MethodGet, "/v1/shows/s-1", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestAuthenticateRejectsExpiredAndInvalidRole(t *testing.T) {
	secret := []byte(strings.Repeat("x", 32))
	handler := authenticate(secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for name, token := range map[string]string{
		"expired":      signedToken(t, secret, "buyer", time.Now().Add(-time.Minute)),
		"invalid role": signedToken(t, secret, "admin", time.Now().Add(time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/shows/s-1", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuthenticateReadsWebSocketQueryToken(t *testing.T) {
	secret := []byte(strings.Repeat("x", 32))
	handler := authenticate(secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	token := signedToken(t, secret, "viewer", time.Now().Add(time.Minute))
	request := httptest.NewRequest(http.MethodGet, "/v1/ws/shows/s-1?token="+token, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestHealthIsPublicAndAPIRequiresAuthentication(t *testing.T) {
	handler := (&API{jwtSecret: []byte(strings.Repeat("x", 32))}).Handler()
	for path, want := range map[string]int{
		"/healthz":  http.StatusOK,
		"/v1/shows": http.StatusUnauthorized,
		"/metrics":  http.StatusOK,
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != want {
				t.Fatalf("got status %d, want %d", response.Code, want)
			}
		})
	}
}

func signedToken(t *testing.T, secret []byte, role string, expires time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
