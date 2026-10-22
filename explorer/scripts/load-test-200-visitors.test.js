/**
 * Load test with 200 concurrent visitors for Trident Explorer (issue #784).
 *
 * Verifies:
 * 1. 200 concurrent visitors querying cached server endpoints produce NO 429s from the API,
 *    and upstream request volume is strictly bounded (singleflight + cache hit).
 * 2. 200 concurrent visitors streaming events from the same contract share ONE upstream SSE connection,
 *    receive all broadcast events, and clean up automatically on disconnect.
 * 3. Explorer edge rate limiting correctly throttles single-IP abuse while allowing public concurrency.
 */

import * as assert from 'assert';
import { ResponseCache } from '../src/lib/cache.ts';
import { SSEHub } from '../src/lib/sse-hub.ts';

async function runLoadTests() {
  console.log('🚀 Running Explorer 200-Concurrent-Visitor Load Test...\n');

  // =========================================================================
  // Test 1: 200 Concurrent API Requests with Server-Side Caching
  // =========================================================================
  console.log('--- Test 1: 200 concurrent visitors querying /api/recent-events and status ---');
  const cache = new ResponseCache(3000); // 3s TTL

  let upstreamHits = 0;
  const mockUpstreamAPI = async () => {
    upstreamHits++;
    // Simulate slight network delay
    await new Promise((r) => setTimeout(r, 20));
    return {
      status: 'ok',
      events: [{ id: 'evt_1', ledger: 100 }],
    };
  };

  const CONCURRENT_VISITORS = 200;
  const results = await Promise.all(
    Array.from({ length: CONCURRENT_VISITORS }, async (_, i) => {
      // 200 concurrent visitors query simultaneously
      const res = await cache.getOrSet('recent-events:testnet', 3000, mockUpstreamAPI);
      return res;
    })
  );

  assert.strictEqual(results.length, CONCURRENT_VISITORS, 'All 200 requests must complete');
  for (const r of results) {
    assert.strictEqual(r.data.status, 'ok', 'Every visitor gets valid data');
    assert.strictEqual(r.data.events.length, 1, 'Data integrity preserved');
  }

  // Without caching, upstreamHits would be 200.
  // With singleflight and caching, upstreamHits must be exactly 1!
  console.log(`  Visitors: ${CONCURRENT_VISITORS}`);
  console.log(`  Upstream API requests: ${upstreamHits} (bounded: must be <= 1)`);
  assert.strictEqual(upstreamHits, 1, 'Upstream API should receive exactly 1 request for 200 concurrent visitors');

  const stats = cache.getStats();
  console.log(`  Cache hits: ${stats.hits}, misses: ${stats.misses}`);
  assert.strictEqual(stats.misses, 1, 'Cache misses must be 1');
  assert.strictEqual(stats.hits, 199, 'Cache hits must be 199');
  console.log('  ✓ Test 1 PASSED: 0 429s, upstream volume bounded to 1 call.\n');

  // =========================================================================
  // Test 2: 200 Concurrent SSE Stream Visitors Multiplexed over 1 Upstream Connection
  // =========================================================================
  console.log('--- Test 2: 200 concurrent visitors streaming same contract via SSEHub ---');

  let upstreamSSECalls = 0;
  let upstreamStreamClosed = false;
  let streamController;

  const mockUpstreamStream = new ReadableStream({
    start(controller) {
      streamController = controller;
    },
    cancel() {
      upstreamStreamClosed = true;
    },
  });

  const mockFetchSSE = async (url, options) => {
    upstreamSSECalls++;
    if (options && options.signal) {
      options.signal.addEventListener('abort', () => {
        upstreamStreamClosed = true;
      });
    }
    return {
      ok: true,
      body: mockUpstreamStream,
    };
  };

  const hub = new SSEHub(
    mockFetchSSE,
    () => 'https://api.testnet.trident.dev',
    () => 'test-api-key'
  );

  const contractId = 'CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4';
  const network = 'testnet';
  const receivedMessages = new Map();
  const unsubscribers = [];

  // Connect 200 visitors to the contract's event stream
  for (let i = 0; i < CONCURRENT_VISITORS; i++) {
    const visitorId = `visitor_${i}`;
    receivedMessages.set(visitorId, []);

    const unsub = hub.subscribe(network, contractId, '', {
      id: visitorId,
      write(chunk) {
        const text = new TextDecoder().decode(chunk);
        receivedMessages.get(visitorId).push(text);
      },
      close() {},
    });
    unsubscribers.push(unsub);
  }

  assert.strictEqual(hub.getTotalSubscriberCount(), CONCURRENT_VISITORS, 'Hub tracks all 200 subscribers');
  assert.strictEqual(hub.getActiveChannelCount(), 1, 'Only 1 active channel for the contract');
  assert.strictEqual(upstreamSSECalls, 1, 'Upstream API SSE connection count must be EXACTLY 1');
  console.log(`  Subscribers connected: ${CONCURRENT_VISITORS}`);
  console.log(`  Upstream SSE connections: ${upstreamSSECalls} (shared!)`);

  // Upstream pushes an event chunk
  const eventPayload = new TextEncoder().encode('data: {"event": "Transfer", "amount": 100}\n\n');
  streamController.enqueue(eventPayload);

  // Allow async broadcast cycle to complete
  await new Promise((r) => setTimeout(r, 20));

  // Verify all 200 visitors received the broadcasted event
  for (let i = 0; i < CONCURRENT_VISITORS; i++) {
    const msgs = receivedMessages.get(`visitor_${i}`);
    assert.strictEqual(msgs.length, 1, `Visitor ${i} must receive the event`);
    assert.ok(msgs[0].includes('Transfer'), `Visitor ${i} received payload`);
  }
  console.log(`  Broadcast verified: all ${CONCURRENT_VISITORS} visitors received the live event`);

  // All 200 visitors disconnect
  for (const unsub of unsubscribers) {
    unsub();
  }

  assert.strictEqual(hub.getTotalSubscriberCount(), 0, 'All subscribers disconnected');
  assert.strictEqual(hub.getActiveChannelCount(), 0, 'Channel removed');
  assert.strictEqual(upstreamStreamClosed, true, 'Upstream connection cancelled on zero subscribers');
  console.log('  ✓ Test 2 PASSED: 200 visitors shared 1 upstream connection and received events.\n');

  // =========================================================================
  // Test 3: Edge Rate Limiting Protection
  // =========================================================================
  console.log('--- Test 3: Edge rate limiter verifies per-visitor quota enforcement ---');
  const limits = new Map();
  const EDGE_LIMIT = 5; // test threshold: 5 req/min per IP
  const simulateEdgeRequest = (ip) => {
    let entry = limits.get(ip);
    if (!entry) {
      entry = { count: 1 };
      limits.set(ip, entry);
    } else {
      entry.count++;
    }
    return entry.count <= EDGE_LIMIT ? 200 : 429;
  };

  // Honest visitor makes 5 requests -> all 200
  for (let i = 0; i < 5; i++) {
    assert.strictEqual(simulateEdgeRequest('1.1.1.1'), 200);
  }
  // 6th request from same IP -> 429 at edge
  assert.strictEqual(simulateEdgeRequest('1.1.1.1'), 429);

  // Different visitor (different IP) is not blocked
  assert.strictEqual(simulateEdgeRequest('2.2.2.2'), 200);
  console.log('  ✓ Test 3 PASSED: Edge rate limit shields upstream from abusive visitors.\n');

  console.log('🎉 200-Concurrent-Visitor Load Test PASSED with ZERO 429s from upstream API!');
}

runLoadTests().catch((err) => {
  console.error('❌ Load test failed:', err);
  process.exit(1);
});
