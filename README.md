# LiveDrop

LiveDrop is a live-commerce backend split into a public **edge** service and a private
**core** service. Edge serves authenticated REST and WebSocket traffic. Core owns show
state, sale windows, inventory, and orders. PostgreSQL is the durable source of truth;
Redis provides atomic inventory reservation, distributed rate limiting, viewer counts,
and cross-edge event fan-out.

The purchase invariant has two independent guards: a Redis Lua script is the only hot
path that decrements cached stock, and a locked PostgreSQL product row rejects any
order that would exceed durable stock. Orders and their fan-out events are committed
in the same database transaction using an outbox, so Redis Pub/Sub delivery can retry
after a core restart.

## Architecture

```text
Clients ── REST / WebSocket ──> Edge ── gRPC ──> Core
                                  │                ├── Redis (Lua stock + Pub/Sub)
                                  └── Redis <──────└── PostgreSQL (orders + outbox)
```

- **Edge** (`cmd/edge`) verifies HS256 JWTs, applies Redis-backed per-subject rate
  limits, serves the REST API, and fans Redis Pub/Sub events to local WebSocket rooms.
- **Core** (`cmd/core`) owns lifecycle transitions and transactions. It is intended
  to be reachable only from edge pods on the private network.
- **PostgreSQL** stores shows, products, sales, orders, and the transactional outbox.
  The core applies idempotent migrations at startup; `make migrate` is also available.
- **Redis** stock keys are seeded from PostgreSQL when absent. A product-row lock and
  stock constraint remain the final oversell guard if the cache is stale or lost.

## Run locally

Requirements: Go 1.22+, Docker with Compose, `make`; `protoc` and the Go protobuf
plugins are only needed when changing `proto/commerce.proto`.

```sh
cp .env.example .env
# Set JWT_SECRET to a private random value of at least 32 bytes.
docker compose up --build -d postgres redis core edge
make migrate
```

The edge API is at `http://localhost:8080`; local gRPC, Postgres, and Redis are
available on `localhost:9090`, `localhost:5432`, and `localhost:6379`. Compose binds
the database and gRPC ports to loopback. `make down` stops the stack; named volumes
preserve local database/cache data.

JWTs are expected from an identity provider trusted to sign HS256 tokens with
`JWT_SECRET`. Tokens must include `sub`, `role` (`seller`, `buyer`, or `viewer`), and
`exp`. The local-only helper prints a one-hour test token; do not use it as an
identity provider or in production:

```sh
JWT_SECRET="$(grep '^JWT_SECRET=' .env | cut -d= -f2-)" \
  go run ./cmd/devtoken -sub local-seller -role seller
```

Run `go test ./...`, `go vet ./...`, and `go build ./cmd/edge ./cmd/core ./cmd/migrate`
to verify the Go code. `make proto` regenerates the checked-in gRPC stubs.

## Configuration

| Variable | Purpose | Default |
|---|---|---|
| `EDGE_HTTP_ADDR` | Public REST/WebSocket listen address | `:8080` |
| `CORE_GRPC_ADDR` | Private core gRPC listen address | `:9090` |
| `CORE_GRPC_TARGET` | Core target used by edge | `localhost:9090` |
| `CORE_METRICS_ADDR` | Core health and Prometheus endpoint | `:9091` |
| `DATABASE_URL` | PostgreSQL connection URL (required by core) | — |
| `REDIS_ADDR` | Redis host and port | `localhost:6379` |
| `REDIS_PASSWORD` | Redis AUTH token, if configured | — |
| `REDIS_TLS` | Enable TLS for managed Redis | `false` |
| `JWT_SECRET` | HS256 verification key, minimum 32 bytes | required by edge |
| `RATE_LIMIT_RPS` | Refilled requests/sec per authenticated subject | `10` |
| `RESERVE_TTL` | Maximum Redis reservation operation timeout (seconds) | `30` |

## REST API

All `/v1` endpoints require a valid bearer JWT, except WebSocket clients may pass the
token in the `token` query parameter.

| Method | Path | Role |
|---|---|---|
| `POST` | `/v1/shows` | seller |
| `POST` | `/v1/shows/{id}/products` | seller |
| `POST` | `/v1/shows/{id}/go-live` | seller |
| `POST` | `/v1/shows/{id}/push-product` | seller |
| `POST` | `/v1/shows/{id}/start-sale` | seller |
| `POST` | `/v1/products/{id}/orders` | buyer |
| `GET` | `/v1/shows/{id}` | any authenticated role |
| `GET` | `/v1/ws/shows/{id}?token=JWT` | any authenticated role |

Create a show with `{"title":"Friday drop"}`. Add a product with
`{"title":"Limited item","price_cents":2500,"total_stock":10}`. A live show can stage
it with `{"product_id":"..."}` and open a sale with
`{"product_id":"...","sale_type":"FLASH","duration_seconds":60}`. An order body is
`{"quantity":1}` and must include a unique `Idempotency-Key` header. Confirmed orders
return `201`, duplicate requests replay the stored result with `200`, exhausted stock
returns `409`, and rate limiting returns `429`.

Events sent to WebSocket clients are JSON frames such as `PRODUCT_PUSHED`,
`SALE_STARTED`, `INVENTORY_UPDATE`, `SOLD_OUT`, and `VIEWER_COUNT`. A slow client with
a full bounded send queue is disconnected rather than blocking its room.

## Operations and testing

- `make test`: unit tests
- `make loadtest`: purchase contention test (requires k6 and a configured active
  product; see [`scripts/loadtest/README.md`](scripts/loadtest/README.md))
- `scripts/loadtest/fanout.js`: maintain WebSocket viewers while a seller stages a
  product
- `/healthz`, `/readyz`, and `/metrics`: edge health/readiness and Prometheus metrics;
  core exposes health and metrics on `CORE_METRICS_ADDR`.

The contention script is a reproducible test harness, not a benchmark report. The
Redis stock cache can be re-seeded after a cache miss; automated stock reconciliation
and reservation expiry are not implemented. The database row lock is therefore the
durable final correctness boundary. Pub/Sub delivery uses an outbox and is at-least
once, so clients should treat event IDs as deduplication keys.
HTTP request IDs are generated by edge, returned in `X-Request-ID`, propagated as gRPC
metadata, and included in structured edge/core request logs. Viewer counts are
best-effort counters and can drift if an edge process terminates without closing its
WebSocket sessions.

## Deployment

- `deploy/terraform/` provisions a VPC, private EKS cluster, encrypted PostgreSQL, and
  TLS-enabled ElastiCache Redis. Supply `database_password` and `redis_auth_token`
  using a secret manager or protected Terraform variables.
- `deploy/k8s/config.yaml` contains edge/core Deployments, Services, probes, HPAs, and
  a NetworkPolicy that limits core gRPC ingress to edge pods. Create the
  `livedrop-secrets` Secret out of band with keys `database-url`, `redis-password`,
  and `jwt-secret`; point the ConfigMap at the provisioned Redis endpoint. After
  installing the AWS Load Balancer Controller and creating an ACM certificate, apply
  the ingress with `ACM_CERTIFICATE_ARN=... sh deploy/k8s/apply.sh`.
- Install the Prometheus Operator and its external-metrics adapter before applying
  `deploy/k8s/monitoring.yaml` or enabling the edge HPA; configure the adapter to
  expose `websocket_active_connections` with the `app` label.
- `.github/workflows/ci.yml` runs tests, vet, and builds on pushes and pull requests.

Terraform and Kubernetes files are deployment starting points; review account,
network, certificate, scaling, and backup policies before applying to production.
