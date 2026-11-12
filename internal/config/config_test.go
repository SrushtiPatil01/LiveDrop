package config

import (
	"strings"
	"testing"
)

func TestLoadEdgeRequiresStrongJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("x", 31))
	if _, err := LoadEdge(); err == nil {
		t.Fatal("LoadEdge accepted a short JWT secret")
	}
}

func TestLoadEdgeDefaultsAndRateLimitBounds(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("RATE_LIMIT_RPS", "10")
	t.Setenv("REDIS_TLS", "false")
	t.Setenv("EDGE_HTTP_ADDR", "")
	t.Setenv("CORE_GRPC_TARGET", "")
	t.Setenv("REDIS_ADDR", "")
	cfg, err := LoadEdge()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.CoreGRPCAddr != "localhost:9090" || cfg.RedisAddr != "localhost:6379" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("RATE_LIMIT_RPS", "10001")
	if _, err := LoadEdge(); err == nil {
		t.Fatal("LoadEdge accepted an excessive rate limit")
	}
}

func TestLoadCoreRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_TLS", "false")
	if _, err := LoadCore(); err == nil {
		t.Fatal("LoadCore accepted an empty database URL")
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/livedrop")
	t.Setenv("RESERVE_TTL", "301")
	if _, err := LoadCore(); err == nil {
		t.Fatal("LoadCore accepted an excessive reservation timeout")
	}
}
