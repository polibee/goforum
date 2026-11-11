// k6 load test for public read paths.
// Usage: BASE_URL=http://127.0.0.1:8080 k6 run scripts/loadtest.js
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const TOPIC = __ENV.TOPIC_PATH || '/';

export const options = {
  scenarios: {
    browse: {
      executor: 'ramping-vus',
      startVUs: 5,
      stages: [
        { duration: '30s', target: 30 },
        { duration: '1m', target: 30 },
        { duration: '15s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
  },
};

const paths = ['/', '/?page=2', '/search?q=test', '/pricing', '/feed.xml', '/sitemap.xml'];

export default function () {
  const path = Math.random() < 0.4 ? TOPIC : paths[Math.floor(Math.random() * paths.length)];
  const res = http.get(BASE + path, { tags: { path } });
  check(res, {
    'status is 200': (r) => r.status === 200,
    'body not empty': (r) => r.body && r.body.length > 0,
  });
  sleep(Math.random() * 1.5 + 0.3);
}
