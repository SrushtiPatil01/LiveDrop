import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const confirmed = new Counter('confirmed_orders');
const buyers = Number(__ENV.BUYERS || 100);
const baseURL = __ENV.EDGE_URL || 'http://localhost:8080';

export const options = {
  scenarios: {
    purchase_race: {
      executor: 'shared-iterations',
      vus: buyers,
      iterations: buyers,
      maxDuration: '2m',
    },
  },
};

export default function () {
  const response = http.post(
    `${baseURL}/v1/products/${__ENV.PRODUCT_ID}/orders`,
    JSON.stringify({ quantity: 1 }),
    {
      headers: {
        Authorization: `Bearer ${__ENV.JWT_TOKEN}`,
        'Content-Type': 'application/json',
        'Idempotency-Key': `load-${__VU}-${__ITER}-${Date.now()}`,
      },
    },
  );
  if (response.status === 201) confirmed.add(1);
  check(response, {
    'order confirmed or inventory exhausted': (r) => r.status === 201 || r.status === 409,
  });
}
