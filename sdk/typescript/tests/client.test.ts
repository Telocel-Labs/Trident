import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TridentClient, TridentError } from "../src/index.js";

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

describe("queryEvents", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetch({ events: [mockEvent], next_cursor: "", has_more: false }));
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

  it("throws TridentError(UNAUTHORIZED) on 401", async () => {
    vi.stubGlobal("fetch", mockFetch("Unauthorized", 401));

    await expect(client.queryEvents({})).rejects.toMatchObject({
      code: "UNAUTHORIZED",
    });
  });

  it("throws TridentError(RATE_LIMITED) on 429", async () => {
    vi.stubGlobal("fetch", mockFetch("Too many requests", 429));

    await expect(client.queryEvents({})).rejects.toMatchObject({
      code: "RATE_LIMITED",
    });
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

  it("throws TridentError(NOT_FOUND) on 404", async () => {
    vi.stubGlobal("fetch", mockFetch("Not found", 404));

    await expect(
      client.getEventById({ id: "00000000-0000-0000-0000-000000000099" }),
    ).rejects.toMatchObject({
      code: "NOT_FOUND",
    });

    const err = await client
      .getEventById({ id: "00000000-0000-0000-0000-000000000099" })
      .catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TridentError);
    expect((err as TridentError).code).toBe("NOT_FOUND");
  });

  it("throws TridentError(UNAUTHORIZED) on 401", async () => {
    vi.stubGlobal("fetch", mockFetch("Unauthorized", 401));

    await expect(
      client.getEventById({ id: "some-id" }),
    ).rejects.toMatchObject({ code: "UNAUTHORIZED" });
  });
});
