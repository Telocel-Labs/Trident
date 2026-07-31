// Load scenario for GET /v1/stats/* (issue #322).
//
// Exercises GET /v1/stats/indexer (lightweight, DB-independent-ish) and
// GET /v1/stats/contracts (Redis-cached, backed by the contract_stats_rollup
// table — see services/api/handlers/stats.go and main.go's rollup refresh
// job) under sustained concurrent load.
//
// SLO thresholds from docs/slo.md (SLO 2 — read routes: p95 < 500ms; SLO 3 —
// 99.5% non-5xx).
//
// Usage:
//   BASE_URL=http://localhost:3000 API_KEY=<key> k6 run load-tests/stats-load.js
//
// Requires k6 (https://k6.io). No external modules.

import http from "k6/http";
import { check, sleep } from "k6";

const BASE_URL = __ENV.BASE_URL || "http://localhost:3000";
const API_KEY = __ENV.API_KEY || "";
const headers = API_KEY ? { "X-API-Key": API_KEY } : {};

export const options = {
  scenarios: {
    indexerStats: {
      executor: "constant-vus",
      exec: "indexerStats",
      vus: Number(__ENV.VUS || 10),
      duration: __ENV.DURATION || "2m",
    },
    contractsStats: {
      executor: "constant-vus",
      exec: "contractsStats",
      vus: Number(__ENV.VUS || 10),
      duration: __ENV.DURATION || "2m",
    },
  },
  thresholds: {
    "http_req_duration{scenario:indexerStats}": ["p(95)<500"],
    "http_req_duration{scenario:contractsStats}": ["p(95)<500"],
    http_req_failed: ["rate<0.005"],
  },
};

export function indexerStats() {
  const res = http.get(`${BASE_URL}/v1/stats/indexer`, { headers });
  check(res, { "indexer stats: status is 200 or 401": (r) => r.status === 200 || r.status === 401 });
  sleep(1);
}

export function contractsStats() {
  const res = http.get(`${BASE_URL}/v1/stats/contracts`, { headers });
  check(res, { "contracts stats: status is 200 or 401": (r) => r.status === 200 || r.status === 401 });
  sleep(1);
}
