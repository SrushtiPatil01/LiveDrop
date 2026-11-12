package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/core/model"
	"github.com/SrushtiPatil01/LiveDrop/internal/id"
	"github.com/SrushtiPatil01/LiveDrop/internal/metrics"
	"github.com/SrushtiPatil01/LiveDrop/internal/observability"
	"github.com/SrushtiPatil01/LiveDrop/internal/store/postgres"
	redisstore "github.com/SrushtiPatil01/LiveDrop/internal/store/redis"
)

type App struct {
	db             *postgres.Store
	redis          *redisstore.Store
	reserveTimeout time.Duration
	workerID       string
	logger         *slog.Logger
}

func New(db *postgres.Store, cache *redisstore.Store, reserveTimeout time.Duration, logger *slog.Logger) (*App, error) {
	workerID, err := id.New()
	if err != nil {
		return nil, err
	}
	return &App{db: db, redis: cache, reserveTimeout: reserveTimeout, workerID: workerID, logger: logger}, nil
}

func (a *App) CreateShow(ctx context.Context, sellerID, title string) (model.Show, error) {
	title = strings.TrimSpace(title)
	if sellerID == "" || !postgres.ValidText(title, 160) {
		return model.Show{}, fmt.Errorf("%w: seller ID and a show title of 1-160 characters are required", model.ErrInvalidArgument)
	}
	showID, err := id.New()
	if err != nil {
		return model.Show{}, err
	}
	return a.db.CreateShow(ctx, model.Show{ID: showID, SellerID: sellerID, Title: title})
}

func (a *App) AddProduct(ctx context.Context, sellerID, showID, title string, priceCents, stock int64) (model.Product, error) {
	title = strings.TrimSpace(title)
	if sellerID == "" || showID == "" || !postgres.ValidText(title, 200) || priceCents <= 0 || stock <= 0 {
		return model.Product{}, fmt.Errorf("%w: seller, show, title, positive price, and positive stock are required", model.ErrInvalidArgument)
	}
	productID, err := id.New()
	if err != nil {
		return model.Product{}, err
	}
	product, err := a.db.AddProduct(ctx, model.Product{
		ID: productID, ShowID: showID, Title: title, PriceCents: priceCents, TotalStock: stock,
	}, sellerID)
	if err != nil {
		return model.Product{}, err
	}
	seeded, err := a.redis.SeedStock(ctx, product.ID, product.Stock)
	if err != nil {
		return model.Product{}, fmt.Errorf("product saved but stock cache could not be seeded: %w", err)
	}
	product.Stock = seeded
	return product, nil
}

func (a *App) GoLive(ctx context.Context, showID, sellerID string) (model.Show, error) {
	if showID == "" || sellerID == "" {
		return model.Show{}, fmt.Errorf("%w: show ID and seller ID are required", model.ErrInvalidArgument)
	}
	eventID, err := id.New()
	if err != nil {
		return model.Show{}, err
	}
	return a.db.GoLive(ctx, showID, sellerID, eventID)
}

func (a *App) PushProduct(ctx context.Context, showID, productID, sellerID string) (model.Show, model.Product, error) {
	if showID == "" || productID == "" || sellerID == "" {
		return model.Show{}, model.Product{}, fmt.Errorf("%w: show ID, product ID, and seller ID are required", model.ErrInvalidArgument)
	}
	eventID, err := id.New()
	if err != nil {
		return model.Show{}, model.Product{}, err
	}
	return a.db.PushProduct(ctx, showID, productID, sellerID, eventID)
}

func (a *App) StartSale(ctx context.Context, showID, productID, sellerID, saleType string, durationSeconds int64) (model.Sale, error) {
	saleType = strings.ToUpper(strings.TrimSpace(saleType))
	if showID == "" || productID == "" || sellerID == "" ||
		(saleType != "FLASH" && saleType != "STANDARD") || durationSeconds < 1 || durationSeconds > 86400 {
		return model.Sale{}, fmt.Errorf("%w: sale requires IDs, FLASH or STANDARD type, and duration between 1 and 86400 seconds", model.ErrInvalidArgument)
	}
	eventID, err := id.New()
	if err != nil {
		return model.Sale{}, err
	}
	return a.db.StartSale(ctx, showID, productID, sellerID, saleType, eventID, durationSeconds)
}

func (a *App) PlaceOrder(ctx context.Context, buyerID, productID, key string, quantity int64) (model.Order, error) {
	if buyerID == "" || productID == "" || len(key) < 8 || len(key) > 128 || quantity < 1 || quantity > 100 {
		return model.Order{}, fmt.Errorf("%w: buyer, product, idempotency key (8-128 chars), and quantity (1-100) are required", model.ErrInvalidArgument)
	}
	existing, found, err := a.db.OrderByIdempotency(ctx, buyerID, key)
	if err != nil {
		return model.Order{}, err
	}
	if found {
		if existing.ProductID != productID || existing.Quantity != quantity {
			return model.Order{}, model.ErrIdempotencyUse
		}
		return existing, nil
	}
	if _, err := a.db.GetProduct(ctx, productID); err != nil {
		return model.Order{}, err
	}

	reserveCtx, cancel := context.WithTimeout(ctx, a.reserveTimeout)
	defer cancel()
	started := time.Now()
	remaining, err := a.redis.Reserve(reserveCtx, productID, quantity)
	metrics.InventoryReserveLatency.Observe(time.Since(started).Seconds())
	if errors.Is(err, redisstore.ErrStockMissing) {
		stock, stockErr := a.db.RemainingStock(ctx, productID)
		if stockErr != nil {
			return model.Order{}, stockErr
		}
		if _, seedErr := a.redis.SeedStock(reserveCtx, productID, stock); seedErr != nil {
			return model.Order{}, seedErr
		}
		started = time.Now()
		remaining, err = a.redis.Reserve(reserveCtx, productID, quantity)
		metrics.InventoryReserveLatency.Observe(time.Since(started).Seconds())
	}
	if errors.Is(err, redisstore.ErrSoldOut) {
		metrics.OversellAttemptsTotal.Inc()
		metrics.OrdersTotal.WithLabelValues("sold_out").Inc()
		return model.Order{}, model.ErrSoldOut
	}
	if err != nil {
		return model.Order{}, err
	}

	orderID, err := id.New()
	if err != nil {
		return model.Order{}, errors.Join(err, a.releaseReservation(productID, quantity))
	}
	eventID, err := id.New()
	if err != nil {
		return model.Order{}, errors.Join(err, a.releaseReservation(productID, quantity))
	}
	soldOutEventID, err := id.New()
	if err != nil {
		return model.Order{}, errors.Join(err, a.releaseReservation(productID, quantity))
	}
	order := model.Order{ID: orderID, ProductID: productID, Quantity: quantity, RemainingStock: remaining}
	committed, duplicate, err := a.db.CommitOrder(ctx, order, buyerID, key, eventID, soldOutEventID)
	if err != nil {
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
		recovered, found, recoveryErr := a.db.OrderByIdempotency(recoveryCtx, buyerID, key)
		recoveryCancel()
		if recoveryErr != nil {
			return model.Order{}, errors.Join(err, fmt.Errorf("commit outcome is unknown; reservation retained: %w", recoveryErr))
		}
		if found && recovered.ProductID == productID && recovered.Quantity == quantity {
			metrics.OrdersTotal.WithLabelValues("confirmed").Inc()
			a.logger.InfoContext(ctx, "order committed", "request_id", observability.RequestID(ctx),
				"order_id", recovered.ID, "product_id", productID, "buyer_id", buyerID, "quantity", quantity)
			return recovered, nil
		}
		if releaseErr := a.releaseReservation(productID, quantity); releaseErr != nil {
			return model.Order{}, errors.Join(err, releaseErr)
		}
		if errors.Is(err, model.ErrSoldOut) {
			metrics.OversellAttemptsTotal.Inc()
			metrics.OrdersTotal.WithLabelValues("sold_out").Inc()
			return model.Order{}, err
		}
		metrics.OrdersTotal.WithLabelValues("failed").Inc()
		return model.Order{}, err
	}
	if duplicate {
		if releaseErr := a.releaseReservation(productID, quantity); releaseErr != nil {
			return model.Order{}, fmt.Errorf("order exists but duplicate reservation cleanup failed: %w", releaseErr)
		}
		committed.Replayed = true
		return committed, nil
	}
	metrics.OrdersTotal.WithLabelValues("confirmed").Inc()
	a.logger.InfoContext(ctx, "order committed", "request_id", observability.RequestID(ctx),
		"order_id", committed.ID, "product_id", productID, "buyer_id", buyerID, "quantity", quantity)
	return committed, nil
}

func (a *App) releaseReservation(productID string, quantity int64) error {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.redis.Release(releaseCtx, productID, quantity); err != nil {
		return fmt.Errorf("release inventory reservation: %w", err)
	}
	return nil
}

func (a *App) Snapshot(ctx context.Context, showID string) (model.Show, []model.Product, int64, error) {
	if showID == "" {
		return model.Show{}, nil, 0, fmt.Errorf("%w: show ID is required", model.ErrInvalidArgument)
	}
	show, products, err := a.db.Snapshot(ctx, showID)
	if err != nil {
		return model.Show{}, nil, 0, err
	}
	viewers, err := a.redis.ViewerCount(ctx, showID)
	if err != nil {
		return model.Show{}, nil, 0, err
	}
	return show, products, viewers, nil
}

func (a *App) RunOutbox(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	nextCleanup := time.Time{}
	for {
		if err := a.publishOutboxBatch(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("outbox delivery failed", "error", err)
		}
		if time.Now().After(nextCleanup) && ctx.Err() == nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := a.db.DeletePublishedOutboxBefore(cleanupCtx, time.Now().Add(-7*24*time.Hour))
			cancel()
			if err != nil {
				a.logger.Error("outbox retention cleanup failed", "error", err)
			}
			nextCleanup = time.Now().Add(time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) publishOutboxBatch(ctx context.Context) error {
	events, err := a.db.ClaimOutbox(ctx, a.workerID, 100)
	if err != nil {
		return err
	}
	for _, event := range events {
		publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := a.redis.Publish(publishCtx, event.ShowID, event.Payload)
		cancel()
		if err != nil {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			releaseErr := a.db.ReleaseOutbox(releaseCtx, event.ID, a.workerID)
			releaseCancel()
			if releaseErr != nil {
				return errors.Join(err, releaseErr)
			}
			return err
		}
		if err := a.db.MarkOutboxPublished(ctx, event.ID, a.workerID); err != nil {
			return err
		}
	}
	return nil
}
