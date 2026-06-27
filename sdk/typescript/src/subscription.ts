import { z } from "zod";
import { SorobanEventSchema } from "./index.js";
import type {
  SorobanEvent,
  SubscribeToContractParams,
  Subscription,
  EventType,
} from "./index.js";

const INITIAL_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 30_000;

/** Inbound WebSocket message schema (matches the Go hub's WriteJSON output). */
const WsEventSchema = z.object({
  contract_id: z.string(),
  ledger_sequence: z.string(),
  ledger_timestamp: z.string(),
  transaction_hash: z.string(),
  event_index: z.string(),
  event_type: z.string(),
  topics: z.string(),
  data: z.string(),
});

function parseWsMessage(raw: unknown): SorobanEvent | null {
  const result = WsEventSchema.safeParse(raw);
  if (!result.success) return null;

  const m = result.data;
  let topics: string[] = [];
  try {
    topics = JSON.parse(m.topics) as string[];
  } catch {
    /* malformed — skip */
  }

  const parsed = SorobanEventSchema.safeParse({
    id: "",
    contract_id: m.contract_id,
    ledger_sequence: parseInt(m.ledger_sequence, 10),
    ledger_timestamp: m.ledger_timestamp,
    transaction_hash: m.transaction_hash,
    event_index: parseInt(m.event_index, 10),
    event_type: m.event_type,
    topics,
    data: (() => {
      try {
        return JSON.parse(m.data);
      } catch {
        return m.data;
      }
    })(),
    created_at: m.ledger_timestamp,
  });

  return parsed.success ? parsed.data : null;
}

/**
 * Opens a WebSocket to {wsUrl} and calls `onEvent` for each matching message.
 * Reconnects with exponential backoff (500ms–30s) on unexpected close.
 * Returns a Subscription whose `unsubscribe()` cancels all reconnects and
 * closes the socket.
 */
export function createSubscription(
  wsUrl: string,
  params: SubscribeToContractParams,
): Subscription {
  let cancelled = false;
  let ws: WebSocket | null = null;
  let backoffMs = INITIAL_BACKOFF_MS;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  function connect(): void {
    if (cancelled) return;

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
      backoffMs = INITIAL_BACKOFF_MS; // reset on successful connect
    };

    ws.onmessage = (evt: MessageEvent) => {
      let raw: unknown;
      try {
        raw = JSON.parse(evt.data as string);
      } catch {
        params.onError?.(new Error(`WebSocket: failed to parse message`));
        return;
      }

      const event = parseWsMessage(raw);
      if (event) {
        params.onEvent(event);
      } else {
        params.onError?.(new Error("WebSocket: received invalid event frame"));
      }
    };

    ws.onerror = () => {
      params.onError?.(new Error("WebSocket connection error"));
    };

    ws.onclose = (evt: CloseEvent) => {
      if (cancelled) return;
      // Unexpected close — schedule reconnect.
      if (!evt.wasClean) {
        scheduleReconnect();
      }
    };
  }

  function scheduleReconnect(): void {
    if (cancelled) return;
    reconnectTimer = setTimeout(() => {
      backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
      connect();
    }, backoffMs);
  }

  connect();

  return {
    unsubscribe(): void {
      cancelled = true;
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      if (ws !== null) {
        ws.close();
        ws = null;
      }
    },
  };
}
