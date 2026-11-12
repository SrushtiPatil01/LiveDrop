package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/config"
	"github.com/SrushtiPatil01/LiveDrop/internal/core/app"
	corerpc "github.com/SrushtiPatil01/LiveDrop/internal/core/rpc"
	"github.com/SrushtiPatil01/LiveDrop/internal/observability"
	"github.com/SrushtiPatil01/LiveDrop/internal/store/postgres"
	redisstore "github.com/SrushtiPatil01/LiveDrop/internal/store/redis"
	commercev1 "github.com/SrushtiPatil01/LiveDrop/proto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("core service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadCore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	cache, err := redisstore.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, 0, cfg.RedisTLS)
	if err != nil {
		return err
	}
	defer cache.Close()
	application, err := app.New(db, cache, cfg.ReserveTimeout, logger)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen for gRPC: %w", err)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(observability.UnaryServerInterceptor(logger)))
	commercev1.RegisterCommerceServiceServer(grpcServer, corerpc.NewServer(application, logger))
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("livedrop.commerce.v1.CommerceService", healthpb.HealthCheckResponse_SERVING)
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		monitorHealth(ctx, healthServer, db, cache, logger)
	}()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.Handler())
	metricsMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	metricsMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(checkCtx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := cache.Ping(checkCtx); err != nil {
			http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	metricsServer := &http.Server{
		Addr: cfg.MetricsAddr, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("core gRPC listening", "address", cfg.GRPCAddr)
		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("serve gRPC: %w", err)
		}
	}()
	go func() {
		logger.Info("core metrics listening", "address", cfg.MetricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve metrics: %w", err)
		}
	}()
	outboxDone := make(chan struct{})
	go func() {
		defer close(outboxDone)
		application.RunOutbox(ctx)
	}()

	var serverErr error
	select {
	case <-ctx.Done():
	case serverErr = <-errCh:
	}
	stop()
	<-outboxDone
	<-healthDone
	healthServer.SetServingStatus("livedrop.commerce.v1.CommerceService", healthpb.HealthCheckResponse_NOT_SERVING)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownPeriod)
	defer cancel()
	metricsShutdownErr := metricsServer.Shutdown(shutdownCtx)
	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
		if serverErr == nil {
			serverErr = fmt.Errorf("graceful gRPC shutdown: %w", shutdownCtx.Err())
		}
	}
	if serverErr != nil {
		return errors.Join(serverErr, metricsShutdownErr)
	}
	return metricsShutdownErr
}

func monitorHealth(ctx context.Context, healthServer *health.Server, db *postgres.Store, cache *redisstore.Store, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := db.Ping(checkCtx)
		if err == nil {
			err = cache.Ping(checkCtx)
		}
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("core dependency health check failed", "error", err)
			}
			healthServer.SetServingStatus("livedrop.commerce.v1.CommerceService", healthpb.HealthCheckResponse_NOT_SERVING)
		} else {
			healthServer.SetServingStatus("livedrop.commerce.v1.CommerceService", healthpb.HealthCheckResponse_SERVING)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
