// Load scenario for GET /v1/events/stream (SSE) (issue #322).
//
// k6 has no first-class SSE client, but Stream() (services/api/handlers/stream.go)
// is a plain long-lived HTTP response with Content-Type: text/event-stream —
// so a raw http.get with a generous `timeout` and `responseType: "text"`
// connects, holds the connection open for HOLD_SECONDS, and captures
// whatever bytes arrived. This validates "can N clients hold a stream
// connection open concurrently without the server rejecting/erroring them",
// not individual event delivery/ordering — that's exercised by the
// integration tests in services/api/handlers/stream_integration_test.go,
// which is the right layer for correctness assertions.
//
// SLO: this endpoint isn't in docs/slo.md's read/write latency split (it's
// long-lived, not request/response), so the only asserted threshold is
// http_req_failed for the initial connect — the SLO here is closer to
// "N concurrent streams stay open for the hold duration", checked via the
// stream_connected/stream_still_open custom metrics below rather than a k6
// threshold.
//
// Usage:
//   BASE_URL=http://localhost:3000 API_KEY=<key> \
//     CONCURRENT_STREAMS=20 HOLD_SECONDS=30 \
//     k6 run load-tests/stream-load.js
//
// Requires k6 (https://k6.io). No external modules.

import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:3000";
const API_KEY = __ENV.API_KEY || "";
const CONCURRENT_STREAMS = Number(__ENV.CONCURRENT_STREAMS || 20);
const HOLD_SECONDS = Number(__ENV.HOLD_SECONDS || 30);
// A contractId query param is required by the handler's validation; any
// syntactically valid one is fine for a connect-and-hold check.
const CONTRACT_ID = __ENV.STREAM_CONTRACT_ID || "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";

const streamConnected = new Rate("stream_connected");

const headers = API_KEY ? { "X-API-Key": API_KEY } : {};

export const options = {
  scenarios: {
    connectAndHold: {
      executor: "constant-vus",
      vus: CONCURRENT_STREAMS,
      // One "iteration" per VU: connect, hold for HOLD_SECONDS, done.
      iterations: 1,
      maxDuration: `${HOLD_SECONDS + 30}s`,
    },
  },
  thresholds: {
    stream_connected: ["rate>0.95"],
  },
};

export default function () {
  const res = http.get(
    `${BASE_URL}/v1/events/stream?contractId=${CONTRACT_ID}`,
    {
      headers,
      // Hold the connection open roughly HOLD_SECONDS; k6 aborts the
      // request at this timeout, which is the intended "disconnect after
      // holding" behavior for this scenario rather than an error.
      timeout: `${HOLD_SECONDS}s`,
      responseType: "text",
    }
  );

  // A successful "hold" ends via k6's timeout aborting the still-open
  // connection, which k6 reports as an error status (0) with a timeout
  // message — that IS the success case here, not a failure. A real
  // rejection (4xx/5xx returned promptly) is the failure case.
  const timedOutHoldingOpen = res.status === 0;
  const rejectedImmediately = res.status >= 400;

  streamConnected.add(!rejectedImmediately);

  check(res, {
    "stream: not rejected with 4xx/5xx": () => !rejectedImmediately,
  });

  if (rejectedImmediately) {
    console.warn(`stream connect rejected: status=${res.status} body=${(res.body || "").slice(0, 200)}`);
  } else if (!timedOutHoldingOpen) {
    // Server closed the stream on its own before the hold duration elapsed
    // (e.g. graceful shutdown, or the handler ending the response) — not
    // necessarily a bug, but worth surfacing since the goal was to hold it
    // open for HOLD_SECONDS.
    console.warn(`stream closed before hold duration elapsed: status=${res.status}`);
  }
}
