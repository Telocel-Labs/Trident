import { useEffect, useRef, useState } from "react";
import type { SorobanEvent, SubscribeToContractParams } from "@trident-indexer/sdk";
import { useTridentClient } from "./context.js";

export interface UseSubscriptionParams {
  contractId: string;
  topic0?: string;
  /** Called on each incoming event in addition to updating `lastEvent`. */
  onEvent?: (event: SorobanEvent) => void;
  /** Called when the WebSocket encounters an error. */
  onError?: (error: Error) => void;
}

export interface UseSubscriptionResult {
  /** The most recently received event, or null before the first event arrives. */
  lastEvent: SorobanEvent | null;
  /** True while the subscription is active. */
  isConnected: boolean;
}

export function useSubscription(
  params: UseSubscriptionParams,
): UseSubscriptionResult {
  const client = useTridentClient();
  const [lastEvent, setLastEvent] = useState<SorobanEvent | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const paramsRef = useRef(params);
  paramsRef.current = params;

  useEffect(() => {
    setIsConnected(true);

    const subscribeParams: SubscribeToContractParams = {
      contractId: paramsRef.current.contractId,
      topic0: paramsRef.current.topic0,
      onEvent: (event) => {
        setLastEvent(event);
        paramsRef.current.onEvent?.(event);
      },
      onError: (error) => {
        paramsRef.current.onError?.(error);
      },
    };

    const subscription = client.subscribeToContract(subscribeParams);

    return () => {
      setIsConnected(false);
      subscription.unsubscribe();
    };
  // Re-subscribe only when the client or contractId changes.
  // topic0 and callbacks are read via ref so they don't cause reconnects.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, params.contractId]);

  return { lastEvent, isConnected };
}
