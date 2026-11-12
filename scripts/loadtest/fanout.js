import { check, sleep } from 'k6';
import ws from 'k6/ws';
import { Counter } from 'k6/metrics';

const receivedEvents = new Counter('websocket_events_received');
const baseURL = (__ENV.EDGE_URL || 'http://localhost:8080').replace(/^http/, 'ws');

export const options = {
  vus: Number(__ENV.VIEWERS || 100),
  duration: `${Number(__ENV.EVENTS || 30)}s`,
};

export default function () {
  const url = `${baseURL}/v1/ws/shows/${__ENV.SHOW_ID}?token=${__ENV.VIEWER_JWT}`;
  const response = ws.connect(url, {}, (socket) => {
    socket.on('message', () => receivedEvents.add(1));
    socket.setTimeout(() => socket.close(), Number(__ENV.EVENTS || 30) * 1000);
    socket.on('error', () => {});
  });
  check(response, { 'WebSocket upgrade succeeds': (r) => r && r.status === 101 });
  sleep(1);
}
