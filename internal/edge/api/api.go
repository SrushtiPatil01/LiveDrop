package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/edge/realtime"
	redisstore "github.com/SrushtiPatil01/LiveDrop/internal/store/redis"
	commercev1 "github.com/SrushtiPatil01/LiveDrop/proto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type API struct {
	core      commercev1.CommerceServiceClient
	health    healthpb.HealthClient
	redis     *redisstore.Store
	hub       *realtime.Hub
	jwtSecret []byte
	rateLimit int
	logger    *slog.Logger
}

func New(core commercev1.CommerceServiceClient, health healthpb.HealthClient, cache *redisstore.Store, hub *realtime.Hub, secret []byte, rateLimit int, logger *slog.Logger) *API {
	return &API{core: core, health: health, redis: cache, hub: hub, jwtSecret: secret, rateLimit: rateLimit, logger: logger}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", a.ready)
	mux.Handle("GET /metrics", promhttp.Handler())
	serviceMux := http.NewServeMux()
	serviceMux.Handle("POST /v1/shows", a.rateLimited(requireRole("seller", http.HandlerFunc(a.createShow))))
	serviceMux.Handle("POST /v1/shows/{id}/products", a.rateLimited(requireRole("seller", http.HandlerFunc(a.addProduct))))
	serviceMux.Handle("POST /v1/shows/{id}/go-live", a.rateLimited(requireRole("seller", http.HandlerFunc(a.goLive))))
	serviceMux.Handle("POST /v1/shows/{id}/push-product", a.rateLimited(requireRole("seller", http.HandlerFunc(a.pushProduct))))
	serviceMux.Handle("POST /v1/shows/{id}/start-sale", a.rateLimited(requireRole("seller", http.HandlerFunc(a.startSale))))
	serviceMux.Handle("POST /v1/products/{id}/orders", a.rateLimited(requireRole("buyer", http.HandlerFunc(a.placeOrder))))
	serviceMux.Handle("GET /v1/shows/{id}", a.rateLimited(http.HandlerFunc(a.getShow)))
	serviceMux.Handle("GET /v1/ws/shows/{id}", a.rateLimited(http.HandlerFunc(a.websocket)))
	mux.Handle("/v1/", authenticate(a.jwtSecret, serviceMux))
	return mux
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.redis.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	response, err := a.health.Check(ctx, &healthpb.HealthCheckRequest{Service: "livedrop.commerce.v1.CommerceService"})
	if err != nil || response == nil || response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		writeError(w, http.StatusServiceUnavailable, "core service is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) rateLimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := currentClaims(r.Context())
		if claims.Subject == "" {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.redis.AllowRequest(r.Context(), claims.Subject, a.rateLimit)
		if err != nil {
			a.logger.Error("rate limiter unavailable", "error", err, "subject", claims.Subject)
			writeError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) createShow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := a.core.CreateShow(r.Context(), &commercev1.CreateShowRequest{
		SellerId: currentClaims(r.Context()).Subject, Title: body.Title,
	})
	if !respondRPC(w, http.StatusCreated, result, err) {
		return
	}
}

func (a *API) addProduct(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string `json:"title"`
		PriceCents int64  `json:"price_cents"`
		TotalStock int64  `json:"total_stock"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := a.core.AddProduct(r.Context(), &commercev1.AddProductRequest{
		SellerId: currentClaims(r.Context()).Subject, ShowId: r.PathValue("id"),
		Title: body.Title, PriceCents: body.PriceCents, TotalStock: body.TotalStock,
	})
	respondRPC(w, http.StatusCreated, result, err)
}

func (a *API) goLive(w http.ResponseWriter, r *http.Request) {
	result, err := a.core.GoLive(r.Context(), &commercev1.ShowRequest{
		ShowId: r.PathValue("id"), SellerId: currentClaims(r.Context()).Subject,
	})
	respondRPC(w, http.StatusOK, result, err)
}

func (a *API) pushProduct(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProductID string `json:"product_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := a.core.PushProduct(r.Context(), &commercev1.PushProductRequest{
		SellerId: currentClaims(r.Context()).Subject, ShowId: r.PathValue("id"), ProductId: body.ProductID,
	})
	respondRPC(w, http.StatusOK, result, err)
}

func (a *API) startSale(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProductID       string `json:"product_id"`
		SaleType        string `json:"sale_type"`
		DurationSeconds int64  `json:"duration_seconds"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := a.core.StartSale(r.Context(), &commercev1.StartSaleRequest{
		SellerId: currentClaims(r.Context()).Subject, ShowId: r.PathValue("id"),
		ProductId: body.ProductID, SaleType: body.SaleType, DurationSeconds: body.DurationSeconds,
	})
	respondRPC(w, http.StatusOK, result, err)
}

func (a *API) placeOrder(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}
	var body struct {
		Quantity int64 `json:"quantity"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := a.core.PlaceOrder(r.Context(), &commercev1.PlaceOrderRequest{
		BuyerId: currentClaims(r.Context()).Subject, ProductId: r.PathValue("id"),
		IdempotencyKey: key, Quantity: body.Quantity,
	})
	if err != nil {
		respondRPC(w, http.StatusCreated, nil, err)
		return
	}
	statusCode := http.StatusCreated
	if result.GetReplayed() {
		statusCode = http.StatusOK
	}
	writeJSON(w, statusCode, result)
}

func (a *API) getShow(w http.ResponseWriter, r *http.Request) {
	result, err := a.core.GetShowSnapshot(r.Context(), &commercev1.ShowRequest{ShowId: r.PathValue("id")})
	respondRPC(w, http.StatusOK, result, err)
}

func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	a.hub.Handle(w, r, r.PathValue("id"))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request must contain one JSON value")
		return false
	}
	return true
}

func respondRPC(w http.ResponseWriter, successStatus int, value any, err error) bool {
	if err == nil {
		writeJSON(w, successStatus, value)
		return true
	}
	code := http.StatusInternalServerError
	if rpcStatus, ok := status.FromError(err); ok {
		switch rpcStatus.Code() {
		case codes.InvalidArgument:
			code = http.StatusBadRequest
		case codes.Unauthenticated:
			code = http.StatusUnauthorized
		case codes.PermissionDenied:
			code = http.StatusForbidden
		case codes.NotFound:
			code = http.StatusNotFound
		case codes.ResourceExhausted, codes.FailedPrecondition, codes.AlreadyExists:
			code = http.StatusConflict
		case codes.Unavailable, codes.DeadlineExceeded:
			code = http.StatusServiceUnavailable
		default:
			code = http.StatusInternalServerError
		}
		writeError(w, code, rpcStatus.Message())
		return false
	}
	writeError(w, code, "internal service error")
	return false
}

func writeJSON(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}
