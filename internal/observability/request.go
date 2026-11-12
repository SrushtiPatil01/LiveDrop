package observability

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/id"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	if value != "" {
		return value
	}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if values := incoming.Get("x-request-id"); len(values) > 0 {
			return values[0]
		}
	}
	return value
}

func HTTPMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := id.New()
		if err != nil {
			http.Error(w, "could not create request ID", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		w.Header().Set("X-Request-ID", requestID)
		logger.Info("HTTP request", "request_id", requestID, "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		requestID := ""
		if incoming, ok := metadata.FromIncomingContext(ctx); ok {
			if values := incoming.Get("x-request-id"); len(values) > 0 {
				requestID = values[0]
			}
		}
		start := time.Now()
		response, err := handler(ctx, req)
		logger.Info("gRPC request", "request_id", requestID, "method", info.FullMethod,
			"duration", time.Since(start), "error", err)
		return response, err
	}
}

func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if requestID := RequestID(ctx); requestID != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
