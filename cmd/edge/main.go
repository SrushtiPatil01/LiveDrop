package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/config"
	"github.com/SrushtiPatil01/LiveDrop/internal/edge/api"
	"github.com/SrushtiPatil01/LiveDrop/internal/edge/realtime"
	"github.com/SrushtiPatil01/LiveDrop/internal/observability"
	redisstore "github.com/SrushtiPatil01/LiveDrop/internal/store/redis"
	commercev1 "github.com/SrushtiPatil01/LiveDrop/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("edge service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadEdge()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cache, err := redisstore.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, 0, cfg.RedisTLS)
	if err != nil {
		return err
	}
	defer cache.Close()
	coreConn, err := grpc.NewClient(cfg.CoreGRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(observability.UnaryClientInterceptor()))
	if err != nil {
		return fmt.Errorf("create core gRPC client: %w", err)
	}
	defer coreConn.Close()
	hub := realtime.NewHub(cache, logger)
	handler := api.New(commercev1.NewCommerceServiceClient(coreConn), healthpb.NewHealthClient(coreConn),
		cache, hub, cfg.JWTSecret, cfg.RateLimitRPS, logger).Handler()
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: observability.HTTPMiddleware(logger, handler), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second,
	}
	subscriberDone := make(chan struct{})
	go func() {
		defer close(subscriberDone)
		hub.RunSubscriber(ctx)
	}()
	errCh := make(chan error, 1)
	go func() {
		logger.Info("edge HTTP listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve edge HTTP: %w", err)
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		stop()
		shutdownErr := shutdown(server, cfg.ShutdownPeriod)
		hub.Close()
		<-subscriberDone
		return errors.Join(err, shutdownErr)
	}
	stop()
	shutdownErr := shutdown(server, cfg.ShutdownPeriod)
	hub.Close()
	<-subscriberDone
	return shutdownErr
}

func shutdown(server *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	return nil
}
