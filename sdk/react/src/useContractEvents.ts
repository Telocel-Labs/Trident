import { useEffect, useReducer, useRef } from "react";
import type { PaginatedEvents, QueryEventsParams, SorobanEvent } from "@trident-indexer/sdk";
import { useTridentClient } from "./context.js";

export interface UseContractEventsParams extends QueryEventsParams {
  /** Refresh interval in milliseconds. Default: no auto-refresh. */
  refreshInterval?: number;
}

export interface UseContractEventsResult {
  events: SorobanEvent[];
  cursor: string | null;
  hasMore: boolean;
  isLoading: boolean;
  error: Error | null;
  /** Manually trigger a re-fetch. */
  refresh: () => void;
}

type State = {
  events: SorobanEvent[];
  cursor: string | null;
  hasMore: boolean;
  isLoading: boolean;
  error: Error | null;
  fetchKey: number;
};

type Action =
  | { type: "FETCH_START" }
  | { type: "FETCH_SUCCESS"; payload: PaginatedEvents }
  | { type: "FETCH_ERROR"; error: Error }
  | { type: "REFRESH" };

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "FETCH_START":
      return { ...state, isLoading: true, error: null };
    case "FETCH_SUCCESS":
      return {
        ...state,
        isLoading: false,
        events: action.payload.events,
        cursor: action.payload.cursor,
        hasMore: action.payload.hasMore,
      };
    case "FETCH_ERROR":
      return { ...state, isLoading: false, error: action.error };
    case "REFRESH":
      return { ...state, fetchKey: state.fetchKey + 1 };
  }
}

const initialState: State = {
  events: [],
  cursor: null,
  hasMore: false,
  isLoading: true,
  error: null,
  fetchKey: 0,
};

export function useContractEvents(
  params: UseContractEventsParams,
): UseContractEventsResult {
  const client = useTridentClient();
  const [state, dispatch] = useReducer(reducer, initialState);
  const paramsRef = useRef(params);
  paramsRef.current = params;

  useEffect(() => {
    let cancelled = false;

    async function fetchEvents() {
      dispatch({ type: "FETCH_START" });
      try {
        const { refreshInterval: _, ...queryParams } = paramsRef.current;
        const result = await client.queryEvents(queryParams);
        if (!cancelled) {
          dispatch({ type: "FETCH_SUCCESS", payload: result });
        }
      } catch (err) {
        if (!cancelled) {
          dispatch({
            type: "FETCH_ERROR",
            error: err instanceof Error ? err : new Error(String(err)),
          });
        }
      }
    }

    fetchEvents();

    const { refreshInterval } = paramsRef.current;
    if (refreshInterval && refreshInterval > 0) {
      const timer = setInterval(fetchEvents, refreshInterval);
      return () => {
        cancelled = true;
        clearInterval(timer);
      };
    }

    return () => {
      cancelled = true;
    };
  // fetchKey is how manual refresh triggers a re-run
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, state.fetchKey]);

  return {
    events: state.events,
    cursor: state.cursor,
    hasMore: state.hasMore,
    isLoading: state.isLoading,
    error: state.error,
    refresh: () => dispatch({ type: "REFRESH" }),
  };
}
