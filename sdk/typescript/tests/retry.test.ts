import { afterEach, describe, expect, it, vi } from "vitest";
import { TridentApiError, TridentClient, TridentError } from "../src/index.js";

const BASE_URL = "http://localhost:3000";
const API_KEY = "test-key";

const mockEvent = {
  id: "00000000-0000-0000-0000-000000000001",
  contract_id: "CTEST",
  ledger_sequence: 100,
  ledger_timestamp: "2024-01-01T00:00:00Z",
  transaction_hash: "abc123",
  event_index: 0,
  event_type: "contract",
  topics: ["transfer"],
  data: '"null"',
  created_at: "2024-01-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

function errorResponse(
  status: number,
  code: string,
  message: string,
  retryAfter?: string,
) {
  const body = JSON.stringify({ error: { code, message } });
  return {
    ok: false,
    status,
    headers: { get: (name: string) => (name.toLowerCase() === "retry-after" ? retryAfter ?? null : null) },
    text: () => Promise.resolve(body),
    json: () => Promise.resolve({}),
  };
}

describe("retry behaviour", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("succeeds after N transient 503s, honouring exponential backoff", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(errorResponse(503, "INTERNAL", "unavailable"))
      .mockResolvedValueOnce(errorResponse(503, "INTERNAL", "unavailable"))
      .mockResolvedValueOnce(
        jsonResponse({ events: [mockEvent], next_cursor: null, has_more: false }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 3, baseDelayMs: 10, jitter: false },
    });

    const resultPromise = client.queryEvents({});
    await vi.runAllTimersAsync();
    const result = await resultPromise;

    expect(result.events).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("honours Retry-After header on 429 instead of computed backoff", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(errorResponse(429, "RATE_LIMITED", "slow down", "2"))
      .mockResolvedValueOnce(
        jsonResponse({ events: [], next_cursor: null, has_more: false }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 3, baseDelayMs: 5000, jitter: false },
    });

    const resultPromise = client.queryEvents({});

    // Only 2000ms (the Retry-After value) should be needed, not the 5000ms
    // base backoff delay — advancing exactly 2000ms must be enough to settle.
    await vi.advanceTimersByTimeAsync(2000);
    const result = await resultPromise;

    expect(result.hasMore).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("gives up after exhausting max attempts and surfaces a typed error", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(errorResponse(503, "INTERNAL", "still down"));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 3, baseDelayMs: 10, jitter: false },
    });

    const resultPromise = client.queryEvents({}).catch((e: unknown) => e);
    await vi.runAllTimersAsync();
    const err = await resultPromise;

    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(503);
    expect((err as TridentApiError).attempts).toBe(3);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("does not retry non-retryable statuses (e.g. 401)", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(errorResponse(401, "UNAUTHORIZED", "bad key"));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 5, baseDelayMs: 10, jitter: false },
    });

    const err = await client.queryEvents({}).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(401);
    expect((err as TridentApiError).attempts).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not retry when disabled at the client level (retry: false)", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(errorResponse(503, "INTERNAL", "down"));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: false,
    });

    const err = await client.queryEvents({}).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).attempts).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("per-call retry option overrides the client-level policy", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(errorResponse(503, "INTERNAL", "down"));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 5, baseDelayMs: 10, jitter: false },
    });

    // Disable retries for just this call.
    const err = await client
      .queryEvents({}, { retry: false })
      .catch((e: unknown) => e);

    expect((err as TridentApiError).attempts).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("wraps a persistently failing network error in a RETRY_EXHAUSTED TridentError", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockRejectedValue(new Error("ECONNRESET"));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 2, baseDelayMs: 10, jitter: false },
    });

    const resultPromise = client.queryEvents({}).catch((e: unknown) => e);
    await vi.runAllTimersAsync();
    const err = await resultPromise;

    expect(err).toBeInstanceOf(TridentError);
    expect((err as TridentError).code).toBe("RETRY_EXHAUSTED");
    expect((err as TridentError).attempts).toBe(2);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("applies retry policy to getEventById as well", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(errorResponse(503, "INTERNAL", "down"))
      .mockResolvedValueOnce(jsonResponse(mockEvent));
    vi.stubGlobal("fetch", fetchMock);

    const client = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      retry: { maxAttempts: 3, baseDelayMs: 10, jitter: false },
    });

    const resultPromise = client.getEventById({ id: mockEvent.id });
    await vi.runAllTimersAsync();
    const event = await resultPromise;

    expect(event.contractId).toBe("CTEST");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
