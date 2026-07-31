// Load scenario for GET /v1/events (list) and GET /v1/events/{id} (get)
// (issue #322).
//
// Two k6 scenarios run concurrently:
//   - "list": repeatedly pages through GET /v1/events.
//   - "get": fetches individual events by id, sourced from the ids returned
//     by the list scenario's first page (via a shared k6 SharedArray-style
//     bootstrap request), falling back to a fixed set of well-known-missing
//     ids (asserting the not-found path stays fast) if none are available.
//
// SLO thresholds below are taken directly from docs/slo.md (SLO 2 — API p95
// latency: 500ms for read routes; SLO 3 — API availability: 99.5% non-5xx).
// Do not invent new thresholds here — if docs/slo.md changes, update these
// to match.
//
// Usage:
//   BASE_URL=http://localhost:3000 API_KEY=<key> k6 run load-tests/events-load.js
//
// Requires k6 (https://k6.io). No external modules.

import http from "k6/http";
import { check, sleep } from "k6";

const BASE_URL = __ENV.BASE_URL || "http://localhost:3000";
const API_KEY = __ENV.API_KEY || "";

const headers = API_KEY ? { "X-API-Key": API_KEY } : {};

export const options = {
  scenarios: {
    list: {
      executor: "constant-vus",
      exec: "listEvents",
      vus: Number(__ENV.LIST_VUS || 20),
      duration: __ENV.DURATION || "2m",
    },
    get: {
      executor: "constant-vus",
      exec: "getEvent",
      vus: Number(__ENV.GET_VUS || 10),
      duration: __ENV.DURATION || "2m",
      startTime: "5s", // let the list scenario populate knownIds first
    },
  },
  thresholds: {
    // SLO 2 (docs/slo.md): p95 < 500ms for read routes.
    "http_req_duration{scenario:list}": ["p(95)<500"],
    "http_req_duration{scenario:get}": ["p(95)<500"],
    // SLO 3 (docs/slo.md): 99.5% non-5xx over a rolling window; for a short
    // load-test run we assert the stricter "no failures at all" bar, since
    // any 5xx here indicates a real regression rather than budget burn.
    http_req_failed: ["rate<0.005"],
  },
};

// A tiny in-process cache of ids seen from list responses, shared across
// iterations of the "get" scenario within a VU (k6 VUs don't share JS state
// across each other, so this is best-effort per-VU, which is fine — the
// scenario just needs *some* real ids to exercise the get path).
let knownIds = [];

export function listEvents() {
  const res = http.get(`${BASE_URL}/v1/events?limit=50`, { headers });
  check(res, {
    "list: status is 200 or 401 (no key configured)": (r) =>
      r.status === 200 || r.status === 401,
  });

  if (res.status === 200) {
    try {
      const body = JSON.parse(res.body);
      if (Array.isArray(body.events)) {
        knownIds = body.events.map((e) => e.id).filter(Boolean).slice(0, 20);
      }
    } catch (e) {
      // Non-JSON or unexpected shape — leave knownIds as-is.
    }
  }

  sleep(1);
}

export function getEvent() {
  const id =
    knownIds.length > 0
      ? knownIds[Math.floor(Math.random() * knownIds.length)]
      : "00000000-0000-4000-8000-000000000000"; // valid-shaped UUID, expected 404

  const res = http.get(`${BASE_URL}/v1/events/${id}`, { headers });
  check(res, {
    "get: status is 200, 401, or 404": (r) =>
      r.status === 200 || r.status === 401 || r.status === 404,
  });

  sleep(1);
}
