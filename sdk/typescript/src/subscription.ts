import { z } from "zod";
import { SorobanEventSchema } from "./index.js";
import type { SorobanEvent, SubscribeToContractParams, Subscription } from "./index.js";

const INITIAL_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 30_000;
const JITTER_FACTOR = 0.2;

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
    contractId: m.contract_id,
    ledgerSequence: parseInt(m.ledger_sequence, 10),
    ledgerTimestamp: m.ledger_timestamp,
    transactionHash: m.transaction_hash,
    eventIndex: parseInt(m.event_index, 10),
    eventType: m.event_type,
    topics,
    data: (() => {
      try {
        return JSON.parse(m.data);
      } catch {
        return m.data;
      }
    })(),
    createdAt: m.ledger_timestamp,
  });

  return parsed.success ? parsed.data : null;
}

function withJitter(ms: number): number {
  return ms + Math.random() * ms * JITTER_FACTOR;
}

function buildWsUrl(baseUrl: string, params: SubscribeToContractParams, lastEventId: string | null): string {
  const url = new URL(baseUrl);
  url.searchParams.set("contractId", params.contractId);
  if (params.topic0) url.searchParams.set("topic0", params.topic0);
  if (lastEventId) url.searchParams.set("cursor", lastEventId);
  return url.toString();
}

/**
 * Opens a WebSocket to {wsUrl} and calls `onEvent` for each matching message.
 * Reconnects with exponential backoff + jitter (500ms–30s) on unexpected close.
 * Tracks the last received event id and resumes from it via a `cursor` query
 * param on reconnect, preventing gaps within the server retention window.
 * Returns a Subscription whose `unsubscribe()` cancels all reconnects and
 * closes the socket.
 */
export function createSubscription(
  wsUrl: string,
  params: SubscribeToContractParams,
  webSocketImpl?: any,
): Subscription {
  let cancelled = false;
  let ws: any = null;
  let backoffMs = INITIAL_BACKOFF_MS;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let lastEventId: string | null = null;

  async function connect(): Promise<void> {
    if (cancelled) return;

    let WS: any;
    try {
      if (webSocketImpl) {
        WS = webSocketImpl;
      } else if (typeof WebSocket !== "undefined") {
        WS = WebSocket;
      } else {
        const wsModule = await import("ws");
        WS = wsModule.default || wsModule;
      }
    } catch (err) {
      params.onError?.(
        new Error(
          "WebSocket is not defined. If you are running in Node.js < 21, you must install the 'ws' package or provide a webSocketImpl in TridentClientConfig.",
        ),
      );
      return;
    }

    if (cancelled) return;

    const connectUrl = buildWsUrl(wsUrl, params, lastEventId);
    const isResume = lastEventId !== null;

    try {
      ws = new WS(connectUrl);
    } catch (err) {
      params.onError?.(err instanceof Error ? err : new Error(String(err)));
      scheduleReconnect();
      return;
    }

    ws.onopen = () => {
      backoffMs = INITIAL_BACKOFF_MS;
      if (isResume && lastEventId) {
        params.onResumed?.(lastEventId);
      } else {
        params.onConnected?.();
      }
    };

    ws.onmessage = (evt: any) => {
      let raw: unknown;
      try {
        raw = JSON.parse(evt.data as string);
      } catch {
        params.onError?.(new Error(`WebSocket: failed to parse message`));
        return;
      }

      const event = parseWsMessage(raw);
      if (event) {
        if (event.id) lastEventId = event.id;
        params.onEvent(event);
      } else {
        params.onError?.(new Error("WebSocket: received invalid event frame"));
      }
    };

    ws.onerror = () => {
      params.onError?.(new Error("WebSocket connection error"));
    };

    ws.onclose = (evt: any) => {
      if (cancelled) return;
      params.onDisconnected?.();
      if (!evt || !evt.wasClean) {
        scheduleReconnect();
      }
    };
  }

  function scheduleReconnect(): void {
    if (cancelled) return;
    const delay = withJitter(backoffMs);
    reconnectTimer = setTimeout(() => {
      backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
      connect();
    }, delay);
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
