CREATE TABLE IF NOT EXISTS shows (
    id TEXT PRIMARY KEY,
    seller_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 160),
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'live', 'ended')),
    current_product_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS products (
    id TEXT PRIMARY KEY,
    show_id TEXT NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    price_cents BIGINT NOT NULL CHECK (price_cents > 0),
    total_stock BIGINT NOT NULL CHECK (total_stock > 0),
    sold_count BIGINT NOT NULL DEFAULT 0 CHECK (sold_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (sold_count <= total_stock)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'shows_current_product_fk'
    ) THEN
        ALTER TABLE shows
            ADD CONSTRAINT shows_current_product_fk
            FOREIGN KEY (current_product_id) REFERENCES products(id)
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS sales (
    id BIGSERIAL PRIMARY KEY,
    show_id TEXT NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    sale_type TEXT NOT NULL CHECK (sale_type IN ('FLASH', 'STANDARD')),
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    CHECK (ends_at > starts_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS sales_one_active_per_product
    ON sales(product_id) WHERE active;

CREATE TABLE IF NOT EXISTS orders (
    id TEXT PRIMARY KEY,
    buyer_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    product_id TEXT NOT NULL REFERENCES products(id),
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    remaining_stock BIGINT NOT NULL CHECK (remaining_stock >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (buyer_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS products_show_id_idx ON products(show_id);
CREATE INDEX IF NOT EXISTS orders_product_id_idx ON orders(product_id);

CREATE TABLE IF NOT EXISTS outbox_events (
    id TEXT PRIMARY KEY,
    show_id TEXT NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    locked_by TEXT,
    locked_until TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox_events(created_at) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_published_idx
    ON outbox_events(published_at) WHERE published_at IS NOT NULL;
