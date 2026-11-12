package rpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SrushtiPatil01/LiveDrop/internal/core/app"
	"github.com/SrushtiPatil01/LiveDrop/internal/core/model"
	commercev1 "github.com/SrushtiPatil01/LiveDrop/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	commercev1.UnimplementedCommerceServiceServer
	app    *app.App
	logger *slog.Logger
}

func NewServer(application *app.App, logger *slog.Logger) *Server {
	return &Server{app: application, logger: logger}
}

func (s *Server) CreateShow(ctx context.Context, req *commercev1.CreateShowRequest) (*commercev1.ShowState, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	show, err := s.app.CreateShow(ctx, req.GetSellerId(), req.GetTitle())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return showToProto(show), nil
}

func (s *Server) AddProduct(ctx context.Context, req *commercev1.AddProductRequest) (*commercev1.Product, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	product, err := s.app.AddProduct(ctx, req.GetSellerId(), req.GetShowId(), req.GetTitle(),
		req.GetPriceCents(), req.GetTotalStock())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return productToProto(product), nil
}

func (s *Server) GoLive(ctx context.Context, req *commercev1.ShowRequest) (*commercev1.ShowState, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	show, err := s.app.GoLive(ctx, req.GetShowId(), req.GetSellerId())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return showToProto(show), nil
}

func (s *Server) PushProduct(ctx context.Context, req *commercev1.PushProductRequest) (*commercev1.ShowState, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	show, _, err := s.app.PushProduct(ctx, req.GetShowId(), req.GetProductId(), req.GetSellerId())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return showToProto(show), nil
}

func (s *Server) StartSale(ctx context.Context, req *commercev1.StartSaleRequest) (*commercev1.SaleState, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	sale, err := s.app.StartSale(ctx, req.GetShowId(), req.GetProductId(), req.GetSellerId(),
		req.GetSaleType(), req.GetDurationSeconds())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return &commercev1.SaleState{
		ShowId: sale.ShowID, ProductId: sale.ProductID, SaleType: sale.SaleType,
		EndsAt: sale.EndsAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, nil
}

func (s *Server) PlaceOrder(ctx context.Context, req *commercev1.PlaceOrderRequest) (*commercev1.PlaceOrderResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	order, err := s.app.PlaceOrder(ctx, req.GetBuyerId(), req.GetProductId(),
		req.GetIdempotencyKey(), req.GetQuantity())
	if err != nil {
		return nil, s.rpcError(err)
	}
	return &commercev1.PlaceOrderResponse{
		OrderId: order.ID, ProductId: order.ProductID, Quantity: order.Quantity,
		RemainingStock: order.RemainingStock, Replayed: order.Replayed,
	}, nil
}

func (s *Server) GetShowSnapshot(ctx context.Context, req *commercev1.ShowRequest) (*commercev1.ShowSnapshot, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	show, products, viewers, err := s.app.Snapshot(ctx, req.GetShowId())
	if err != nil {
		return nil, s.rpcError(err)
	}
	result := &commercev1.ShowSnapshot{Show: showToProto(show), Viewers: viewers}
	for _, product := range products {
		result.Products = append(result.Products, productToProto(product))
	}
	return result, nil
}

func showToProto(show model.Show) *commercev1.ShowState {
	return &commercev1.ShowState{
		Id: show.ID, SellerId: show.SellerID, Title: show.Title, Status: show.Status,
		CurrentProductId: show.CurrentProductID,
	}
}

func productToProto(product model.Product) *commercev1.Product {
	return &commercev1.Product{
		Id: product.ID, ShowId: product.ShowID, Title: product.Title, PriceCents: product.PriceCents,
		Stock: product.Stock, TotalStock: product.TotalStock, SoldCount: product.SoldCount, OnStage: product.OnStage,
	}
}

func (s *Server) rpcError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, model.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, model.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, model.ErrSoldOut):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, model.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, model.ErrInvalidState), errors.Is(err, model.ErrIdempotencyUse):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		s.logger.Error("commerce RPC failed", "error", err)
		return status.Error(codes.Internal, "internal commerce error")
	}
}
