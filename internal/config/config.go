package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Edge struct {
	HTTPAddr       string
	CoreGRPCAddr   string
	RedisAddr      string
	RedisPassword  string
	RedisTLS       bool
	JWTSecret      []byte
	RateLimitRPS   int
	ShutdownPeriod time.Duration
}

type Core struct {
	GRPCAddr       string
	MetricsAddr    string
	DatabaseURL    string
	RedisAddr      string
	RedisPassword  string
	RedisTLS       bool
	ReserveTimeout time.Duration
	ShutdownPeriod time.Duration
}

func LoadEdge() (Edge, error) {
	rps, err := envInt("RATE_LIMIT_RPS", 10)
	if err != nil {
		return Edge{}, err
	}
	if rps < 1 || rps > 10000 {
		return Edge{}, errors.New("RATE_LIMIT_RPS must be between 1 and 10000")
	}
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return Edge{}, errors.New("JWT_SECRET must be set to at least 32 bytes")
	}
	redisTLS, err := envBool("REDIS_TLS", false)
	if err != nil {
		return Edge{}, err
	}
	return Edge{
		HTTPAddr:       env("EDGE_HTTP_ADDR", ":8080"),
		CoreGRPCAddr:   env("CORE_GRPC_TARGET", "localhost:9090"),
		RedisAddr:      env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisTLS:       redisTLS,
		JWTSecret:      []byte(secret),
		RateLimitRPS:   rps,
		ShutdownPeriod: 10 * time.Second,
	}, nil
}

func LoadCore() (Core, error) {
	reserveSeconds, err := envInt("RESERVE_TTL", 30)
	if err != nil {
		return Core{}, err
	}
	if reserveSeconds < 1 || reserveSeconds > 300 {
		return Core{}, errors.New("RESERVE_TTL must be between 1 and 300 seconds")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Core{}, errors.New("DATABASE_URL is required")
	}
	redisTLS, err := envBool("REDIS_TLS", false)
	if err != nil {
		return Core{}, err
	}
	return Core{
		GRPCAddr:       env("CORE_GRPC_ADDR", ":9090"),
		MetricsAddr:    env("CORE_METRICS_ADDR", ":9091"),
		DatabaseURL:    databaseURL,
		RedisAddr:      env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisTLS:       redisTLS,
		ReserveTimeout: time.Duration(reserveSeconds) * time.Second,
		ShutdownPeriod: 15 * time.Second,
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", key, err)
	}
	return parsed, nil
}

func envInt(key string, fallback int) (int, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}
