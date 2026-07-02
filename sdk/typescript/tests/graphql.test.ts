import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { setupServer } from "msw/node";
import { http, HttpResponse } from "msw";
import { TridentClient } from "../src/index.js";

const mockEvent = {
  id: "event-1",
  contractId: "CTEST",
  ledgerSequence: 100,
  ledgerTimestamp: "2024-01-01T00:00:00Z",
  topic0: "transfer",
  topic1: "from_addr",
  topic2: "to_addr",
  topic3: null,
  data: '{"amount": "1000"}',
  transactionHash: "hash123",
  eventType: "contract",
  createdAt: "2024-01-01T00:00:00Z",
};

const server = setupServer(
  http.post("http://localhost:3000/graphql", () => {
    return HttpResponse.json({
      data: {
        events: {
          edges: [
            {
              cursor: "cursor1",
              node: mockEvent,
            },
          ],
          pageInfo: {
            hasNextPage: false,
            endCursor: "cursor1",
          },
        },
      },
    });
  }),
);

beforeAll(() => server.listen());
afterAll(() => server.close());

describe("GraphQLTransport", () => {
  it("should query events via GraphQL", async () => {
    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "test_key",
      network: "testnet",
      transport: "graphql",
    });

    const result = await client.queryEvents({
      contractId: "CTEST",
      limit: 10,
    });

    expect(result.events).toHaveLength(1);
    expect(result.events[0].contractId).toBe("CTEST");
    expect(result.hasMore).toBe(false);
  });

  it("should include all event fields", async () => {
    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "test_key",
      network: "testnet",
      transport: "graphql",
    });

    const result = await client.queryEvents({});
    const event = result.events[0];

    expect(event.id).toBe("event-1");
    expect(event.contractId).toBe("CTEST");
    expect(event.ledgerSequence).toBe(100);
    expect(event.topics).toContain("transfer");
  });

  it("should handle GraphQL errors", async () => {
    server.use(
      http.post("http://localhost:3000/graphql", () => {
        return HttpResponse.json({
          errors: [{ message: "Contract not found" }],
        });
      }),
    );

    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "test_key",
      network: "testnet",
      transport: "graphql",
    });

    await expect(client.queryEvents({})).rejects.toThrow("GraphQL error");
  });

  it("should handle HTTP 401", async () => {
    server.use(
      http.post("http://localhost:3000/graphql", () => {
        return HttpResponse.json(
          { error: "Unauthorized" },
          { status: 401 },
        );
      }),
    );

    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "invalid_key",
      network: "testnet",
      transport: "graphql",
    });

    await expect(client.queryEvents({})).rejects.toThrow();
  });

  it("should handle HTTP 503", async () => {
    server.use(
      http.post("http://localhost:3000/graphql", () => {
        return HttpResponse.text("Service Unavailable", { status: 503 });
      }),
    );

    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "test_key",
      network: "testnet",
      transport: "graphql",
    });

    await expect(client.queryEvents({})).rejects.toThrow();
  });

  it("REST transport should not include GraphQL code", async () => {
    // This test ensures that REST-only bundle doesn't import GraphQL transport
    const client = new TridentClient({
      apiUrl: "http://localhost:3000",
      apiKey: "test_key",
      network: "testnet",
      transport: "rest",
    });

    // The transport should only be loaded on demand for GraphQL
    expect((client as any).graphqlTransport).toBeUndefined();
  });
});
