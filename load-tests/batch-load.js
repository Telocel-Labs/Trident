// Load scenario for POST /v1/events/batch (issue #322).
//
// Drives a steady stream of batch lookups, each requesting a mix of
// (likely-missing) UUIDs — batch is inherently a "mostly not found" shape
// under load-testing with no seeded corpus, so the assertion is on
// well-formed responses and latency/error-rate SLOs, not on hit rate.
//
// SLO thresholds come directly from docs/slo.md (SLO 2 — write/heavier
// routes: p95 < 1s, since batch invokes gRPC GetEvent N times in parallel;
// SLO 3 — 99.5% non-5xx).
//
// Usage:
//   BASE_URL=http://localhost:3000 API_KEY=<key> k6 run load-tests/batch-load.js
//
// Requires k6 (https://k6.io). No external modules.

import http from "k6/http";
import { check, sleep } from "k6";

const BASE_URL = __ENV.BASE_URL || "http://localhost:3000";
const API_KEY = __ENV.API_KEY || "";
const BATCH_SIZE = Number(__ENV.BATCH_SIZE || 20); // under batchEventsMaxIDs (100)

const headers = Object.assign(
  { "Content-Type": "application/json" },
  API_KEY ? { "X-API-Key": API_KEY } : {}
);

export const options = {
  scenarios: {
    batch: {
      executor: "constant-vus",
      vus: Number(__ENV.VUS || 15),
      duration: __ENV.DURATION || "2m",
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<1000"],
    http_req_failed: ["rate<0.005"],
  },
};

function randomUUID() {
  // RFC 4122 v4-shaped UUID, good enough for exercising the batch validation
  // + gRPC fan-out path without needing a real seeded id corpus.
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === "x" ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

export default function () {
  const ids = Array.from({ length: BATCH_SIZE }, randomUUID);
  const res = http.post(
    `${BASE_URL}/v1/events/batch`,
    JSON.stringify({ ids }),
    { headers }
  );

  check(res, {
    "batch: status is 200 or 401": (r) => r.status === 200 || r.status === 401,
  });

  sleep(1);
}
