package model

import (
	"errors"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("forbidden")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrInvalidState    = errors.New("invalid state")
	ErrSoldOut         = errors.New("sold out")
	ErrIdempotencyUse  = errors.New("idempotency key reused with different request")
)

type Show struct {
	ID               string `json:"id"`
	SellerID         string `json:"seller_id"`
	Title            string `json:"title"`
	Status           string `json:"status"`
	CurrentProductID string `json:"current_product_id,omitempty"`
}

type Product struct {
	ID         string `json:"id"`
	ShowID     string `json:"show_id"`
	Title      string `json:"title"`
	PriceCents int64  `json:"price_cents"`
	Stock      int64  `json:"stock"`
	TotalStock int64  `json:"total_stock"`
	SoldCount  int64  `json:"sold_count"`
	OnStage    bool   `json:"on_stage"`
}

type Sale struct {
	ShowID    string    `json:"show_id"`
	ProductID string    `json:"product_id"`
	SaleType  string    `json:"sale_type"`
	EndsAt    time.Time `json:"ends_at"`
}

type Order struct {
	ID             string `json:"id"`
	ProductID      string `json:"product_id"`
	Quantity       int64  `json:"quantity"`
	RemainingStock int64  `json:"remaining_stock"`
	Replayed       bool   `json:"replayed"`
}

type Event struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	ShowID    string     `json:"show_id,omitempty"`
	ProductID string     `json:"product_id,omitempty"`
	Product   *Product   `json:"product,omitempty"`
	SaleType  string     `json:"sale_type,omitempty"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	Stock     *int64     `json:"stock,omitempty"`
	Count     *int64     `json:"count,omitempty"`
}
