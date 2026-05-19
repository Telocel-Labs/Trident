import { z } from "zod";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

export type Network = "mainnet" | "testnet" | "futurenet";

export interface TridentClientConfig {
  apiUrl: string;
  apiKey: string;
  network: Network;
}

// ---------------------------------------------------------------------------
// Domain types (mirrors SorobanEvent on the server side)
// ---------------------------------------------------------------------------

export const EventTypeSchema = z.enum(["contract", "system", "diagnostic"]);
export type EventType = z.infer<typeof EventTypeSchema>;

export const SorobanEventSchema = z.object({
  id: z.string().uuid(),
  contractId: z.string(),
  ledgerSequence: z.number().int().nonnegative(),
  ledgerTimestamp: z.string().datetime(),
  transactionHash: z.string(),
  eventIndex: z.number().int().nonnegative(),
  eventType: EventTypeSchema,
  topics: z.array(z.string()),
  data: z.unknown(),
  createdAt: z.string().datetime(),
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
// Client
// ---------------------------------------------------------------------------

export class TridentClient {
  private readonly config: TridentClientConfig;

  constructor(config: TridentClientConfig) {
    this.config = config;
  }

  /**
   * Query historical Soroban events with optional filtering.
   *
   * Supports filtering by contract address, topic values, and ledger range.
   * Results are cursor-paginated — pass the returned `cursor` as `after` on
   * the next call to fetch the next page. Returns events in ascending ledger
   * order.
   */
  async queryEvents(params: QueryEventsParams): Promise<PaginatedEvents> {
    void params;
    throw new Error("not yet implemented");
  }

  /**
   * Fetch a single event by its UUID.
   *
   * Returns the full `SorobanEvent` record, or throws if no event with the
   * given id exists.
   */
  async getEventById(params: GetEventByIdParams): Promise<SorobanEvent> {
    void params;
    throw new Error("not yet implemented");
  }

  /**
   * Open a real-time WebSocket subscription to events emitted by a contract.
   *
   * Calls `onEvent` for every new event that matches the subscription criteria
   * as it lands on-chain. Calls `onError` on connection failure and attempts
   * to reconnect automatically. Returns a `Subscription` handle whose
   * `unsubscribe()` method tears down the WebSocket cleanly.
   */
  subscribeToContract(params: SubscribeToContractParams): Subscription {
    void params;
    throw new Error("not yet implemented");
  }
}
