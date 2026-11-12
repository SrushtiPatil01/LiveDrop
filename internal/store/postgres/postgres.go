package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SrushtiPatil01/LiveDrop/internal/core/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = 30
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(72419831)`); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(72419831)`); err != nil {
			slog.Error("release database migration lock", "error", err)
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer closeCancel()
			_ = conn.Conn().Close(closeCtx)
		}
	}()
	for _, name := range entries {
		raw, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) CreateShow(ctx context.Context, show model.Show) (model.Show, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO shows (id, seller_id, title) VALUES ($1, $2, $3)
		RETURNING id, seller_id, title, status`,
		show.ID, show.SellerID, show.Title,
	).Scan(&show.ID, &show.SellerID, &show.Title, &show.Status)
	if err != nil {
		return model.Show{}, fmt.Errorf("create show: %w", err)
	}
	return show, nil
}

func (s *Store) AddProduct(ctx context.Context, product model.Product, sellerID string) (model.Product, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Product{}, fmt.Errorf("begin add product: %w", err)
	}
	defer tx.Rollback(ctx)
	var owner string
	if err := tx.QueryRow(ctx, `SELECT seller_id FROM shows WHERE id = $1 FOR UPDATE`, product.ShowID).Scan(&owner); err != nil {
		return model.Product{}, notFound(err, "show")
	}
	if owner != sellerID {
		return model.Product{}, model.ErrForbidden
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO products (id, show_id, title, price_cents, total_stock)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, show_id, title, price_cents, total_stock, sold_count`,
		product.ID, product.ShowID, product.Title, product.PriceCents, product.TotalStock,
	).Scan(&product.ID, &product.ShowID, &product.Title, &product.PriceCents, &product.TotalStock, &product.SoldCount)
	if err != nil {
		return model.Product{}, fmt.Errorf("insert product: %w", err)
	}
	product.Stock = product.TotalStock - product.SoldCount
	if err := tx.Commit(ctx); err != nil {
		return model.Product{}, fmt.Errorf("commit add product: %w", err)
	}
	return product, nil
}

func (s *Store) GoLive(ctx context.Context, showID, sellerID, eventID string) (model.Show, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Show{}, fmt.Errorf("begin go-live transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var show model.Show
	err = tx.QueryRow(ctx, `
		UPDATE shows SET status = 'live', updated_at = now()
		WHERE id = $1 AND seller_id = $2 AND status = 'draft'
		RETURNING id, seller_id, title, status, COALESCE(current_product_id, '')`,
		showID, sellerID,
	).Scan(&show.ID, &show.SellerID, &show.Title, &show.Status, &show.CurrentProductID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Show{}, s.showTransitionError(ctx, showID, sellerID)
		}
		return model.Show{}, fmt.Errorf("go live: %w", err)
	}
	if err := enqueueEvent(ctx, tx, eventID, showID, model.Event{ID: eventID, Type: "SHOW_LIVE", ShowID: showID}); err != nil {
		return model.Show{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Show{}, fmt.Errorf("commit go-live transaction: %w", err)
	}
	return show, nil
}

func (s *Store) PushProduct(ctx context.Context, showID, productID, sellerID, eventID string) (model.Show, model.Product, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Show{}, model.Product{}, fmt.Errorf("begin product push: %w", err)
	}
	defer tx.Rollback(ctx)
	var show model.Show
	err = tx.QueryRow(ctx, `
		SELECT id, seller_id, title, status, COALESCE(current_product_id, '')
		FROM shows WHERE id = $1 FOR UPDATE`, showID,
	).Scan(&show.ID, &show.SellerID, &show.Title, &show.Status, &show.CurrentProductID)
	if err != nil {
		return model.Show{}, model.Product{}, notFound(err, "show")
	}
	if show.SellerID != sellerID {
		return model.Show{}, model.Product{}, model.ErrForbidden
	}
	if show.Status != "live" {
		return model.Show{}, model.Product{}, model.ErrInvalidState
	}
	var product model.Product
	err = tx.QueryRow(ctx, `
		SELECT id, show_id, title, price_cents, total_stock, sold_count
		FROM products WHERE id = $1 AND show_id = $2`,
		productID, showID,
	).Scan(&product.ID, &product.ShowID, &product.Title, &product.PriceCents, &product.TotalStock, &product.SoldCount)
	if err != nil {
		return model.Show{}, model.Product{}, notFound(err, "product")
	}
	product.Stock = product.TotalStock - product.SoldCount
	product.OnStage = true
	if _, err := tx.Exec(ctx, `
		UPDATE shows SET current_product_id = $2, updated_at = now() WHERE id = $1`,
		showID, productID,
	); err != nil {
		return model.Show{}, model.Product{}, fmt.Errorf("stage product: %w", err)
	}
	if err := enqueueEvent(ctx, tx, eventID, showID, model.Event{
		ID: eventID, Type: "PRODUCT_PUSHED", ShowID: showID, ProductID: productID, Product: &product,
	}); err != nil {
		return model.Show{}, model.Product{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Show{}, model.Product{}, fmt.Errorf("commit product push: %w", err)
	}
	show.CurrentProductID = productID
	return show, product, nil
}

func (s *Store) StartSale(ctx context.Context, showID, productID, sellerID, saleType, eventID string, durationSeconds int64) (model.Sale, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Sale{}, fmt.Errorf("begin sale: %w", err)
	}
	defer tx.Rollback(ctx)
	var owner, currentProduct, status string
	err = tx.QueryRow(ctx, `
		SELECT seller_id, COALESCE(current_product_id, ''), status
		FROM shows WHERE id = $1 FOR UPDATE`, showID,
	).Scan(&owner, &currentProduct, &status)
	if err != nil {
		return model.Sale{}, notFound(err, "show")
	}
	if owner != sellerID {
		return model.Sale{}, model.ErrForbidden
	}
	if status != "live" || currentProduct != productID {
		return model.Sale{}, model.ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `UPDATE sales SET active = false WHERE product_id = $1 AND active`, productID); err != nil {
		return model.Sale{}, fmt.Errorf("close previous sale: %w", err)
	}
	var sale model.Sale
	err = tx.QueryRow(ctx, `
		INSERT INTO sales (show_id, product_id, sale_type, ends_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4::double precision))
		RETURNING show_id, product_id, sale_type, ends_at`,
		showID, productID, saleType, durationSeconds,
	).Scan(&sale.ShowID, &sale.ProductID, &sale.SaleType, &sale.EndsAt)
	if err != nil {
		return model.Sale{}, fmt.Errorf("create sale: %w", err)
	}
	if err := enqueueEvent(ctx, tx, eventID, showID, model.Event{
		ID: eventID, Type: "SALE_STARTED", ShowID: showID, ProductID: productID,
		SaleType: sale.SaleType, EndsAt: &sale.EndsAt,
	}); err != nil {
		return model.Sale{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Sale{}, fmt.Errorf("commit sale: %w", err)
	}
	return sale, nil
}

func (s *Store) GetProduct(ctx context.Context, productID string) (model.Product, error) {
	var product model.Product
	err := s.pool.QueryRow(ctx, `
		SELECT id, show_id, title, price_cents, total_stock, sold_count,
			total_stock - sold_count
		FROM products WHERE id = $1`, productID,
	).Scan(&product.ID, &product.ShowID, &product.Title, &product.PriceCents,
		&product.TotalStock, &product.SoldCount, &product.Stock)
	if err != nil {
		return model.Product{}, notFound(err, "product")
	}
	return product, nil
}

func (s *Store) Snapshot(ctx context.Context, showID string) (model.Show, []model.Product, error) {
	var show model.Show
	err := s.pool.QueryRow(ctx, `
		SELECT id, seller_id, title, status, COALESCE(current_product_id, '')
		FROM shows WHERE id = $1`, showID,
	).Scan(&show.ID, &show.SellerID, &show.Title, &show.Status, &show.CurrentProductID)
	if err != nil {
		return model.Show{}, nil, notFound(err, "show")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, show_id, title, price_cents, total_stock, sold_count, total_stock - sold_count
		FROM products WHERE show_id = $1 ORDER BY created_at`, showID)
	if err != nil {
		return model.Show{}, nil, fmt.Errorf("query show products: %w", err)
	}
	defer rows.Close()
	products := make([]model.Product, 0)
	for rows.Next() {
		var product model.Product
		if err := rows.Scan(&product.ID, &product.ShowID, &product.Title, &product.PriceCents,
			&product.TotalStock, &product.SoldCount, &product.Stock); err != nil {
			return model.Show{}, nil, fmt.Errorf("scan show product: %w", err)
		}
		product.OnStage = product.ID == show.CurrentProductID
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return model.Show{}, nil, fmt.Errorf("iterate show products: %w", err)
	}
	return show, products, nil
}

func (s *Store) OrderByIdempotency(ctx context.Context, buyerID, key string) (model.Order, bool, error) {
	var order model.Order
	err := s.pool.QueryRow(ctx, `
		SELECT id, product_id, quantity, remaining_stock
		FROM orders WHERE buyer_id = $1 AND idempotency_key = $2`,
		buyerID, key,
	).Scan(&order.ID, &order.ProductID, &order.Quantity, &order.RemainingStock)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, false, nil
	}
	if err != nil {
		return model.Order{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	order.Replayed = true
	return order, true, nil
}

func (s *Store) CommitOrder(ctx context.Context, order model.Order, buyerID, key, eventID, soldOutEventID string) (model.Order, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Order{}, false, fmt.Errorf("begin order transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, found, err := orderByKeyTx(ctx, tx, buyerID, key)
	if err != nil {
		return model.Order{}, false, err
	}
	if found {
		if existing.ProductID != order.ProductID || existing.Quantity != order.Quantity {
			return model.Order{}, false, model.ErrIdempotencyUse
		}
		existing.Replayed = true
		return existing, true, tx.Commit(ctx)
	}

	var lockedShowID, showStatus, currentProduct string
	err = tx.QueryRow(ctx, `
		SELECT id, status, COALESCE(current_product_id, '')
		FROM shows WHERE id = (SELECT show_id FROM products WHERE id = $1) FOR UPDATE`, order.ProductID,
	).Scan(&lockedShowID, &showStatus, &currentProduct)
	if err != nil {
		return model.Order{}, false, notFound(err, "product")
	}
	var stock int64
	err = tx.QueryRow(ctx, `
		SELECT total_stock - sold_count FROM products WHERE id = $1 FOR UPDATE`, order.ProductID,
	).Scan(&stock)
	if err != nil {
		return model.Order{}, false, notFound(err, "product")
	}
	var activeSale bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT ends_at > now() FROM sales WHERE product_id = $1 AND active), false)`,
		order.ProductID,
	).Scan(&activeSale); err != nil {
		return model.Order{}, false, fmt.Errorf("validate sale window: %w", err)
	}

	existing, found, err = orderByKeyTx(ctx, tx, buyerID, key)
	if err != nil {
		return model.Order{}, false, err
	}
	if found {
		if existing.ProductID != order.ProductID || existing.Quantity != order.Quantity {
			return model.Order{}, false, model.ErrIdempotencyUse
		}
		existing.Replayed = true
		return existing, true, tx.Commit(ctx)
	}
	if showStatus != "live" || currentProduct != order.ProductID || !activeSale {
		return model.Order{}, false, model.ErrInvalidState
	}
	if order.Quantity <= 0 || stock < order.Quantity {
		return model.Order{}, false, model.ErrSoldOut
	}
	order.RemainingStock = stock - order.Quantity
	tag, err := tx.Exec(ctx, `
		INSERT INTO orders (id, buyer_id, idempotency_key, product_id, quantity, remaining_stock)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (buyer_id, idempotency_key) DO NOTHING`,
		order.ID, buyerID, key, order.ProductID, order.Quantity, order.RemainingStock)
	if err != nil {
		return model.Order{}, false, fmt.Errorf("insert order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		existing, found, err = orderByKeyTx(ctx, tx, buyerID, key)
		if err != nil {
			return model.Order{}, false, err
		}
		if !found {
			return model.Order{}, false, errors.New("idempotency conflict without stored order")
		}
		if existing.ProductID != order.ProductID || existing.Quantity != order.Quantity {
			return model.Order{}, false, model.ErrIdempotencyUse
		}
		existing.Replayed = true
		return existing, true, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE products SET sold_count = sold_count + $2 WHERE id = $1 AND sold_count + $2 <= total_stock`,
		order.ProductID, order.Quantity); err != nil {
		return model.Order{}, false, fmt.Errorf("update sold count: %w", err)
	}
	stockRemaining := order.RemainingStock
	updateEvent := model.Event{
		ID: eventID, Type: "INVENTORY_UPDATE", ShowID: lockedShowID,
		ProductID: order.ProductID, Stock: &stockRemaining,
	}
	if err := enqueueEvent(ctx, tx, eventID, updateEvent.ShowID, updateEvent); err != nil {
		return model.Order{}, false, err
	}
	if order.RemainingStock == 0 {
		soldOutStock := int64(0)
		if err := enqueueEvent(ctx, tx, soldOutEventID, updateEvent.ShowID, model.Event{
			ID: soldOutEventID, Type: "SOLD_OUT", ShowID: updateEvent.ShowID,
			ProductID: order.ProductID, Stock: &soldOutStock,
		}); err != nil {
			return model.Order{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Order{}, false, fmt.Errorf("commit order transaction: %w", err)
	}
	return order, false, nil
}

func orderByKeyTx(ctx context.Context, tx pgx.Tx, buyerID, key string) (model.Order, bool, error) {
	var order model.Order
	err := tx.QueryRow(ctx, `
		SELECT id, product_id, quantity, remaining_stock
		FROM orders WHERE buyer_id = $1 AND idempotency_key = $2`,
		buyerID, key,
	).Scan(&order.ID, &order.ProductID, &order.Quantity, &order.RemainingStock)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, false, nil
	}
	if err != nil {
		return model.Order{}, false, fmt.Errorf("read order in transaction: %w", err)
	}
	return order, true, nil
}

type OutboxEvent struct {
	ID      string
	ShowID  string
	Payload model.Event
}

func enqueueEvent(ctx context.Context, tx pgx.Tx, eventID, showID string, event model.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (id, show_id, payload) VALUES ($1, $2, $3)`,
		eventID, showID, payload); err != nil {
		return fmt.Errorf("enqueue event: %w", err)
	}
	return nil
}

func (s *Store) ClaimOutbox(ctx context.Context, workerID string, limit int) ([]OutboxEvent, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT id FROM outbox_events
			WHERE published_at IS NULL AND (locked_until IS NULL OR locked_until < now())
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox_events
		SET locked_by = $1, locked_until = now() + interval '30 seconds'
		FROM candidates
		WHERE outbox_events.id = candidates.id
		RETURNING outbox_events.id, outbox_events.show_id, outbox_events.payload`, workerID, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()
	events := make([]OutboxEvent, 0)
	for rows.Next() {
		var event OutboxEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &event.ShowID, &payload); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, fmt.Errorf("decode outbox event %s: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}
	return events, nil
}

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID, workerID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE outbox_events SET published_at = now(), locked_by = NULL, locked_until = NULL
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`,
		eventID, workerID)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("outbox event lease expired before acknowledgement")
	}
	return nil
}

func (s *Store) ReleaseOutbox(ctx context.Context, eventID, workerID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE outbox_events SET locked_by = NULL, locked_until = NULL
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`,
		eventID, workerID)
	if err != nil {
		return fmt.Errorf("release outbox event lease: %w", err)
	}
	return nil
}

func (s *Store) DeletePublishedOutboxBefore(ctx context.Context, cutoff time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM outbox_events WHERE published_at IS NOT NULL AND published_at < $1`, cutoff); err != nil {
		return fmt.Errorf("prune published outbox events: %w", err)
	}
	return nil
}

func (s *Store) RemainingStock(ctx context.Context, productID string) (int64, error) {
	var stock int64
	err := s.pool.QueryRow(ctx, `
		SELECT total_stock - sold_count FROM products WHERE id = $1`, productID,
	).Scan(&stock)
	if err != nil {
		return 0, notFound(err, "product")
	}
	return stock, nil
}

func (s *Store) showTransitionError(ctx context.Context, showID, sellerID string) error {
	var owner, status string
	err := s.pool.QueryRow(ctx, `SELECT seller_id, status FROM shows WHERE id = $1`, showID).Scan(&owner, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect show state: %w", err)
	}
	if owner != sellerID {
		return model.ErrForbidden
	}
	return model.ErrInvalidState
}

func notFound(err error, entity string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", model.ErrNotFound, entity)
	}
	return err
}

func ValidText(value string, max int) bool {
	return value != "" && len(strings.TrimSpace(value)) <= max
}
