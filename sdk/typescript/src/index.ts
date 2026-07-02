import { z } from "zod";
import { parseApiError, TridentApiError, TridentError } from "./errors.js";
import { createSubscription } from "./subscription.js";

export { TridentError, TridentApiError } from "./errors.js";
export type { TridentErrorCode } from "./errors.js";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

export type Network = "mainnet" | "testnet" | "futurenet";
export type TransportType = "rest" | "graphql";

export interface TridentClientConfig {
  apiUrl: string;
  apiKey: string;
  network: Network;
  webSocketImpl?: any;
  transport?: TransportType;
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

export const EventTypeSchema = z.enum(["contract", "system", "diagnostic"]);
export type EventType = z.infer<typeof EventTypeSchema>;

export const SorobanEventSchema = z.object({
  id: z.string(),
  contractId: z.string(),
  ledgerSequence: z.number().int().nonnegative(),
  ledgerTimestamp: z.string(),
  transactionHash: z.string(),
  eventIndex: z.number().int().nonnegative(),
  eventType: EventTypeSchema,
  topics: z.array(z.string()),
  data: z.unknown(),
  createdAt: z.string(),
});
export type SorobanEvent = z.infer<typeof SorobanEventSchema>;

// ---------------------------------------------------------------------------
// Query parameter types
// ---------------------------------------------------------------------------

export interface QueryEventsParams {
  contractId?: string;
  topic0?: string;
  topic1?: string;
  ledgerFrom?: number;
  ledgerTo?: number;
  after?: string;
  limit?: number;
  eventType?: "contract" | "system" | "diagnostic";
}

export interface GetEventByIdParams {
  id: string;
}

export interface SubscribeToContractParams {
  contractId: string;
  topic0?: string;
  onEvent: (event: SorobanEvent) => void;
  onError?: (error: Error) => void;
}

export interface Subscription {
  unsubscribe: () => void;
}

export interface PaginatedEvents {
  events: SorobanEvent[];
  cursor: string | null;
  hasMore: boolean;
}

// ---------------------------------------------------------------------------
// Internal API response schemas (snake_case, as returned by the Go API)
// ---------------------------------------------------------------------------

const ApiEventSchema = z.object({
  id: z.string(),
  contract_id: z.string(),
  ledger_sequence: z.number().int().nonnegative(),
  ledger_timestamp: z.string(),
  transaction_hash: z.string(),
  event_index: z.number().int().nonnegative(),
  event_type: z.string(),
  topics: z.array(z.string()),
  data: z.string(),
  created_at: z.string(),
});

const ApiListEventsResponseSchema = z.object({
  events: z.array(ApiEventSchema),
  next_cursor: z.string().nullable(),
  has_more: z.boolean(),
});

function apiEventToSorobanEvent(
  e: z.infer<typeof ApiEventSchema>,
): SorobanEvent {
  return SorobanEventSchema.parse({
    id: e.id,
    contractId: e.contract_id,
    ledgerSequence: e.ledger_sequence,
    ledgerTimestamp: e.ledger_timestamp,
    transactionHash: e.transaction_hash,
    eventIndex: e.event_index,
    eventType: e.event_type,
    topics: e.topics,
    data: (() => {
      try {
        return JSON.parse(e.data);
      } catch {
        return e.data;
      }
    })(),
    createdAt: e.created_at,
  });
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export class TridentClient {
  private readonly config: TridentClientConfig;
  private readonly transport: "rest" | "graphql";
  private graphqlTransport?: any; // Lazy-loaded GraphQL transport

  constructor(config: TridentClientConfig) {
    this.config = config;
    this.transport = config.transport ?? "rest";
  }

  private get headers(): Record<string, string> {
    return {
      "X-API-Key": this.config.apiKey,
      "Content-Type": "application/json",
    };
  }

  private async fetchJSON<T>(
    url: string,
    schema: z.ZodType<T>,
  ): Promise<T> {
    let res: Response;
    try {
      res = await fetch(url, { headers: this.headers });
    } catch (cause) {
      throw new TridentError("INTERNAL", "Network request failed", cause);
    }

    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw parseApiError(res.status, body);
    }

    const json: unknown = await res.json().catch((cause: unknown) => {
      throw new TridentError("INTERNAL", "Failed to parse response JSON", cause);
    });

    return schema.parse(json);
  }

  private async getGraphQLTransport() {
    if (this.graphqlTransport) {
      return this.graphqlTransport;
    }
    // Lazy load GraphQL transport only when needed
    const { GraphQLTransport } = await import("./transports/graphql.js");
    this.graphqlTransport = new GraphQLTransport(this.config.apiUrl, this.config.apiKey);
    return this.graphqlTransport;
  }

  /**
   * Query historical Soroban events with optional filtering.
   *
   * Results are cursor-paginated — pass the returned `cursor` as `after` on
   * the next call to fetch the next page.
   */
  async queryEvents(params: QueryEventsParams): Promise<PaginatedEvents> {
    if (this.transport === "graphql") {
      const transport = await this.getGraphQLTransport();
      return transport.queryEvents(
        params.contractId,
        params.topic0,
        params.topic1,
        params.ledgerFrom,
        params.ledgerTo,
        params.limit,
        params.after,
      );
    }

    // REST transport (default)
    const qs = new URLSearchParams();
    if (params.contractId) qs.set("contractId", params.contractId);
    if (params.topic0) qs.set("topic0", params.topic0);
    if (params.topic1) qs.set("topic1", params.topic1);
    if (params.ledgerFrom !== undefined)
      qs.set("ledgerFrom", String(params.ledgerFrom));
    if (params.ledgerTo !== undefined)
      qs.set("ledgerTo", String(params.ledgerTo));
    if (params.after) qs.set("cursor", params.after);
    if (params.limit !== undefined) qs.set("limit", String(params.limit));
    if (params.eventType) qs.set("event_type", params.eventType);

    const url = `${this.config.apiUrl}/v1/events?${qs.toString()}`;
    const resp = await this.fetchJSON(url, ApiListEventsResponseSchema);

    return {
      events: resp.events.map(apiEventToSorobanEvent),
      cursor: resp.next_cursor,
      hasMore: resp.has_more,
    };
  }

  /**
   * Fetch a single event by its UUID.
   *
   * Throws `TridentError` with code `NOT_FOUND` if no event exists.
   */
  async getEventById(params: GetEventByIdParams): Promise<SorobanEvent> {
    if (this.transport === "graphql") {
      const transport = await this.getGraphQLTransport();
      return transport.getEventById(params.id);
    }

    // REST transport (default)
    const url = `${this.config.apiUrl}/v1/events/${encodeURIComponent(params.id)}`;
    const apiEvent = await this.fetchJSON(url, ApiEventSchema);
    return apiEventToSorobanEvent(apiEvent);
  }

  /**
   * Open a real-time WebSocket subscription to events emitted by a contract.
   *
   * For GraphQL transport, requires graphql-ws to be installed.
   * For REST transport, uses native WebSocket.
   */
  subscribeToContract(params: SubscribeToContractParams): Subscription {
    if (params.topic0 !== undefined && params.topic0 === "") {
      throw new TridentApiError(
        400,
        "INVALID_ARGUMENT",
        "topic0 must not be an empty string; omit the field to receive all events",
      );
    }

    if (this.transport === "graphql") {
      // GraphQL subscriptions require graphql-ws
      try {
        // Attempt to import graphql-ws
        require("graphql-ws");
      } catch {
        throw new TridentError(
          "INTERNAL",
          "GraphQL subscriptions require graphql-ws. Install it with: npm install graphql-ws",
        );
      }

      // Use graphql-ws protocol for subscriptions
      // This will be implemented via the graphql-ws client library
      throw new TridentError(
        "NOT_IMPLEMENTED",
        "GraphQL subscriptions are not yet fully implemented",
      );
    }

    // REST transport (default) - use native WebSocket
    const wsBase = this.config.apiUrl
      .replace(/^https:\/\//, "wss://")
      .replace(/^http:\/\//, "ws://");

    const qs = new URLSearchParams({ contractId: params.contractId });
    if (params.topic0) qs.set("topic0", params.topic0);

    const wsUrl = `${wsBase}/ws?${qs.toString()}`;
    return createSubscription(wsUrl, params, this.config.webSocketImpl);
  }
}
