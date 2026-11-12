package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type claimsKey struct{}

type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

func authenticate(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenString := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if tokenString == "" && strings.HasPrefix(r.URL.Path, "/v1/ws/shows/") {
			tokenString = r.URL.Query().Get("token")
		}
		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("unexpected JWT signing method")
			}
			return secret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
		if err != nil || token == nil || !token.Valid || claims.Subject == "" ||
			(claims.Role != "seller" && claims.Role != "buyer" && claims.Role != "viewer") {
			writeError(w, http.StatusUnauthorized, "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
	})
}

func currentClaims(ctx context.Context) Claims {
	claims, _ := ctx.Value(claimsKey{}).(*Claims)
	if claims == nil {
		return Claims{}
	}
	return *claims
}

func requireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := currentClaims(r.Context())
		if claims.Subject == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if claims.Role != role {
			writeError(w, http.StatusForbidden, "insufficient role")
			return
		}
		next.ServeHTTP(w, r)
	})
}
