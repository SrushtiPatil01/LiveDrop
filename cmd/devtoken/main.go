package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/edge/api"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	subject := flag.String("sub", "", "subject/user ID")
	role := flag.String("role", "", "seller, buyer, or viewer")
	flag.Parse()
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		fmt.Fprintln(os.Stderr, "JWT_SECRET must be set to at least 32 bytes")
		os.Exit(2)
	}
	if *subject == "" || (*role != "seller" && *role != "buyer" && *role != "viewer") {
		fmt.Fprintln(os.Stderr, "usage: JWT_SECRET=... go run ./cmd/devtoken -sub USER_ID -role seller|buyer|viewer")
		os.Exit(2)
	}
	claims := api.Claims{
		Role: *role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   *subject,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(token)
}
