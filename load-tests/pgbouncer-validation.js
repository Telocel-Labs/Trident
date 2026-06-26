// PgBouncer validation load test (issue #87).
//
// Drives 100 concurrent virtual users, each making 10 requests to
// GET /v1/events, and asserts:
//   - no "too many connections" (or other 5xx) errors, and
//   - p99 latency < 500ms.
//
// Run it twice — once with the stack pointed at PgBouncer and once pointed
// straight at Postgres (max_connections=100) — to demonstrate that pooling
// removes the connection-exhaustion errors.
//
// Usage:
//   BASE_URL=http://localhost:3000 k6 run load-tests/pgbouncer-validation.js
//
// Requires k6 (https://k6.io). No external modules.

import http from "k6/http";
import { check } from "k6";
import { Counter } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:3000";

// 100 concurrent clients each making 10 requests = 1000 iterations total.
const CONCURRENT_CLIENTS = Number(__ENV.VUS || 100);
const REQUESTS_PER_CLIENT = Number(__ENV.REQS || 10);

// Tracks the specific failure mode this test exists to catch.
const tooManyConnections = new Counter("too_many_connections_errors");

export const options = {
  scenarios: {
    fanout: {
      executor: "per-vu-iterations",
      vus: CONCURRENT_CLIENTS,
      iterations: REQUESTS_PER_CLIENT,
      maxDuration: "2m",
    },
  },
  thresholds: {
    // Acceptance criteria from issue #87.
    http_req_duration: ["p(99)<500"],
    too_many_connections_errors: ["count==0"],
    http_req_failed: ["rate==0"],
  },
};

export default function () {
  const res = http.get(`${BASE_URL}/v1/events?limit=50`);

  const body = (res.body || "").toLowerCase();
  if (res.status >= 500 && body.includes("too many connections")) {
    tooManyConnections.add(1);
  }

  check(res, {
    "status is 200": (r) => r.status === 200,
    "no connection exhaustion": (r) =>
      !((r.body || "").toLowerCase().includes("too many connections")),
  });
}
