import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TridentApiError, TridentClient, TridentError } from "../src/index.js";

const BASE_URL = "http://localhost:3000";
const API_KEY = "test-key";

const client = new TridentClient({
  apiUrl: BASE_URL,
  apiKey: API_KEY,
  network: "testnet",
});

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

function mockFetch(
  body: unknown,
  status = 200,
): ReturnType<typeof vi.fn> {
  return vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(String(body)),
  });
}

function mockApiErrorFetch(status: number, code: string, message: string, field?: string) {
  const body = JSON.stringify({ error: { code, message, ...(field ? { field } : {}) } });
  return vi.fn().mockResolvedValue({
    ok: false,
    status,
    text: () => Promise.resolve(body),
    json: () => Promise.resolve({}),
  });
}

describe("queryEvents", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetch({ events: [mockEvent], next_cursor: null, has_more: false }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("calls the correct URL with contractId param", async () => {
    await client.queryEvents({ contractId: "CTEST", limit: 10 });

    const [url] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/v1/events");
    expect(url).toContain("contractId=CTEST");
    expect(url).toContain("limit=10");
  });

  it("sets X-API-Key header", async () => {
    await client.queryEvents({});

    const [, init] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    const headers = init.headers as Record<string, string>;
    expect(headers["X-API-Key"]).toBe(API_KEY);
  });

  it("maps snake_case response to camelCase SorobanEvent", async () => {
    const result = await client.queryEvents({});
    expect(result.events).toHaveLength(1);
    expect(result.events[0].contractId).toBe("CTEST");
    expect(result.events[0].ledgerSequence).toBe(100);
    expect(result.hasMore).toBe(false);
    expect(result.cursor).toBeNull();
  });

  it("returns hasMore=false and cursor=null on last page", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({ events: [mockEvent], next_cursor: null, has_more: false }),
    );
    const result = await client.queryEvents({});
    expect(result.hasMore).toBe(false);
    expect(result.cursor).toBeNull();
  });

  it("returns hasMore=true and non-null cursor when more pages exist", async () => {
    const cursorToken = "eyJ2IjoxLCJ0IjoiMDAwMDAwMTIzNDU2In0";
    vi.stubGlobal(
      "fetch",
      mockFetch({ events: [mockEvent], next_cursor: cursorToken, has_more: true }),
    );
    const result = await client.queryEvents({ limit: 1 });
    expect(result.hasMore).toBe(true);
    expect(result.cursor).toBe(cursorToken);
  });

  it("pagination: page 1 has_more=true, page 2 has_more=false", async () => {
    const cursor1 = "cursor-after-page-1";

    // Page 1: returns 3 events with has_more=true
    vi.stubGlobal(
      "fetch",
      mockFetch({ events: [mockEvent, mockEvent, mockEvent], next_cursor: cursor1, has_more: true }),
    );
    const page1 = await client.queryEvents({ limit: 3 });
    expect(page1.events).toHaveLength(3);
    expect(page1.hasMore).toBe(true);
    expect(page1.cursor).toBe(cursor1);

    // Page 2: use returned cursor, gets 2 events with has_more=false
    vi.stubGlobal(
      "fetch",
      mockFetch({ events: [mockEvent, mockEvent], next_cursor: null, has_more: false }),
    );
    const page2 = await client.queryEvents({ limit: 3, after: page1.cursor! });
    expect(page2.events).toHaveLength(2);
    expect(page2.hasMore).toBe(false);
    expect(page2.cursor).toBeNull();

    // Verify cursor was passed as query param
    const [url] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toContain(`cursor=${cursor1}`);
  });

  it("throws TridentApiError on 401 with status and code", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(401, "UNAUTHORIZED", "Unauthorized"));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(401);
    expect((err as TridentApiError).code).toBe("UNAUTHORIZED");
  });

  it("throws TridentApiError on 429", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(429, "RATE_LIMITED", "Too many requests"));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(429);
    expect((err as TridentApiError).code).toBe("RATE_LIMITED");
  });
});

describe("getEventById", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("calls GET /v1/events/{id} and returns parsed event", async () => {
    vi.stubGlobal("fetch", mockFetch(mockEvent));

    const event = await client.getEventById({ id: mockEvent.id });

    const [url] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toContain(`/v1/events/${mockEvent.id}`);
    expect(event.contractId).toBe("CTEST");
    expect(event).toBeInstanceOf(Object);
  });

  it("throws TridentApiError(NOT_FOUND) on 404", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(404, "NOT_FOUND", "Not found"));

    const err = await client
      .getEventById({ id: "00000000-0000-0000-0000-000000000099" })
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(404);
    expect((err as TridentApiError).code).toBe("NOT_FOUND");
  });

  it("throws TridentApiError(UNAUTHORIZED) on 401", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(401, "UNAUTHORIZED", "Unauthorized"));

    const err = await client
      .getEventById({ id: "some-id" })
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(401);
    expect((err as TridentApiError).code).toBe("UNAUTHORIZED");
  });
});

// ── TridentApiError envelope parsing (#133) ───────────────────────────────────

describe("TridentApiError", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("parses structured error envelope — 422 with field", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(422, "INVALID_ARGUMENT", "bad cursor", "cursor"));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(422);
    expect((err as TridentApiError).code).toBe("INVALID_ARGUMENT");
    expect((err as TridentApiError).field).toBe("cursor");
    expect((err as TridentApiError).message).toBe("bad cursor");
  });

  it("parses structured error envelope — 500", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(500, "INTERNAL", "unexpected error"));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(500);
    expect((err as TridentApiError).code).toBe("INTERNAL");
  });

  it("falls back to INTERNAL when body is not JSON", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      text: () => Promise.resolve("<html>Service Unavailable</html>"),
      json: () => Promise.reject(new Error("not json")),
    }));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TridentApiError);
    expect((err as TridentApiError).status).toBe(503);
    expect((err as TridentApiError).code).toBe("INTERNAL");
  });

  it("instanceof TridentApiError works correctly", async () => {
    vi.stubGlobal("fetch", mockApiErrorFetch(401, "UNAUTHORIZED", "bad key"));

    const err = await client.queryEvents({}).catch((e: unknown) => e);
    expect(err instanceof TridentApiError).toBe(true);
  });
});

// ── event_type URL param (#157) ───────────────────────────────────────────────

describe("queryEvents event_type param", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetch({ events: [], next_cursor: "", has_more: false }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("includes event_type in URL when provided", async () => {
    await client.queryEvents({ eventType: "contract" });

    const [url] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toContain("event_type=contract");
  });

  it("omits event_type from URL when not provided", async () => {
    await client.queryEvents({});

    const [url] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).not.toContain("event_type");
  });
});

// ── subscribeToContract topic0 (#142) ────────────────────────────────────────

describe("subscribeToContract", () => {
  it("includes topic0 in WebSocket URL when provided", () => {
    let capturedUrl = "";
    const MockWS = class {
      constructor(url: string) { capturedUrl = url; }
      onopen = null; onmessage = null; onerror = null; onclose = null;
      close() {}
    };
    vi.stubGlobal("WebSocket", MockWS);

    client.subscribeToContract({
      contractId: "CTEST",
      topic0: "transfer",
      onEvent: () => {},
    });

    expect(capturedUrl).toContain("contractId=CTEST");
    expect(capturedUrl).toContain("topic0=transfer");
    vi.unstubAllGlobals();
  });

  it("omits topic0 from URL when not provided", () => {
    let capturedUrl = "";
    const MockWS = class {
      constructor(url: string) { capturedUrl = url; }
      onopen = null; onmessage = null; onerror = null; onclose = null;
      close() {}
    };
    vi.stubGlobal("WebSocket", MockWS);

    client.subscribeToContract({ contractId: "CTEST", onEvent: () => {} });

    expect(capturedUrl).toContain("contractId=CTEST");
    expect(capturedUrl).not.toContain("topic0");
    vi.unstubAllGlobals();
  });

  it("URL-encodes special characters in topic0", () => {
    let capturedUrl = "";
    const MockWS = class {
      constructor(url: string) { capturedUrl = url; }
      onopen = null; onmessage = null; onerror = null; onclose = null;
      close() {}
    };
    vi.stubGlobal("WebSocket", MockWS);

    client.subscribeToContract({
      contractId: "CTEST",
      topic0: "transfer/swap",
      onEvent: () => {},
    });

    expect(capturedUrl).toContain("topic0=transfer%2Fswap");
    vi.unstubAllGlobals();
  });

  it("throws TridentApiError(INVALID_ARGUMENT) for empty topic0", () => {
    expect(() =>
      client.subscribeToContract({ contractId: "CTEST", topic0: "", onEvent: () => {} }),
    ).toThrow(TridentApiError);

    expect(() =>
      client.subscribeToContract({ contractId: "CTEST", topic0: "", onEvent: () => {} }),
    ).toThrowError(expect.objectContaining({ code: "INVALID_ARGUMENT" }));
  });

  it("falls back to importing 'ws' when global WebSocket is not defined", async () => {
    vi.stubGlobal("WebSocket", undefined);

    let capturedUrl = "";
    const MockWS = class {
      constructor(url: string) {
        capturedUrl = url;
      }
      onopen = null;
      onmessage = null;
      onerror = null;
      onclose = null;
      close() {}
    };

    vi.doMock("ws", () => {
      return {
        default: MockWS,
      };
    });

    client.subscribeToContract({
      contractId: "CTEST",
      onEvent: () => {},
    });

    // Wait a bit for the async import of "ws" to resolve
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(capturedUrl).toContain("contractId=CTEST");
    vi.unstubAllGlobals();
    vi.doUnmock("ws");
  });

  it("uses webSocketImpl when provided in config", () => {
    let capturedUrl = "";
    const CustomMockWS = class {
      constructor(url: string) {
        capturedUrl = url;
      }
      onopen = null;
      onmessage = null;
      onerror = null;
      onclose = null;
      close() {}
    };

    const clientWithCustomWS = new TridentClient({
      apiUrl: BASE_URL,
      apiKey: API_KEY,
      network: "testnet",
      webSocketImpl: CustomMockWS,
    });

    vi.stubGlobal("WebSocket", class {
      constructor() {
        throw new Error("Should not use global WebSocket");
      }
    });

    clientWithCustomWS.subscribeToContract({
      contractId: "CTEST",
      onEvent: () => {},
    });

    expect(capturedUrl).toContain("contractId=CTEST");
    vi.unstubAllGlobals();
  });
});
