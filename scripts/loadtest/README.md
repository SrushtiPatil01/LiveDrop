# Load tests

These are repeatable smoke/load tests, not published benchmark results. Run them
against a dedicated local stack; the contention test creates real orders.

```sh
EDGE_URL=http://localhost:8080 \
JWT_TOKEN='<buyer JWT>' \
PRODUCT_ID='<live product ID>' \
BUYERS=100 \
k6 run scripts/loadtest/contention.js
```

The supplied JWT must have a `buyer` role and a unique, stable `sub`. Use a product
with an active sale and set its available stock to the expected number of confirmed
orders. The script checks that only HTTP 201 and 409 responses occur; compare the
`confirmed_orders` metric with the product's stock in the database to assert zero
oversell. Replaying a run requires a fresh product or restoring its inventory.

The WebSocket script maintains the requested number of connections and counts event
frames. Connect a seller to the same show and stage a product while the test runs.
`EVENTS` controls the duration in seconds.
