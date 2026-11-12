package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	OrdersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "orders_total",
		Help: "Purchase requests by outcome.",
	}, []string{"result"})
	OversellAttemptsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "oversell_attempts_total",
		Help: "Inventory reservations rejected because no stock remained.",
	})
	InventoryReserveLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "inventory_reserve_latency_seconds",
		Help:    "Time spent attempting an atomic Redis inventory reservation.",
		Buckets: prometheus.DefBuckets,
	})
	WebsocketActiveConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "websocket_active_connections",
		Help: "Number of WebSocket connections on this edge instance.",
	})
)
