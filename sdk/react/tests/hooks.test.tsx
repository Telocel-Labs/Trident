import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { SorobanEvent, PaginatedEvents } from "@trident-indexer/sdk";

// ---------------------------------------------------------------------------
// Mock @trident-indexer/sdk
// Vitest hoists vi.mock calls, so we cannot reference outer variables inside
// the factory. Instead, expose stable mock functions on the class prototype
// so every test can spy on and configure them via the class.
// ---------------------------------------------------------------------------

vi.mock("@trident-indexer/sdk", () => {
  const queryEvents = vi.fn<() => Promise<PaginatedEvents>>();
  const subscribeToContract = vi.fn();

  class MockTridentClient {
    static __queryEvents = queryEvents;
    static __subscribeToContract = subscribeToContract;
    queryEvents = queryEvents;
    subscribeToContract = subscribeToContract;
  }

  return {
    TridentClient: MockTridentClient,
  };
});

import { TridentClient } from "@trident-indexer/sdk";
import { TridentProvider } from "../src/context.js";
import { useContractEvents } from "../src/useContractEvents.js";
import { useSubscription } from "../src/useSubscription.js";

// ---------------------------------------------------------------------------
// Helpers — grab stable mock references from the MockTridentClient statics
// ---------------------------------------------------------------------------
const MockClient = TridentClient as unknown as {
  __queryEvents: ReturnType<typeof vi.fn>;
  __subscribeToContract: ReturnType<typeof vi.fn>;
  new (...args: unknown[]): unknown;
};

const mockQueryEvents = MockClient.__queryEvents as ReturnType<typeof vi.fn<() => Promise<PaginatedEvents>>>;
const mockSubscribeToContract = MockClient.__subscribeToContract;

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const sampleEvent: SorobanEvent = {
  id: "evt-1",
  contractId: "CTEST",
  ledgerSequence: 100,
  ledgerTimestamp: "2024-01-01T00:00:00Z",
  transactionHash: "abc123",
  eventIndex: 0,
  eventType: "contract",
  topics: ["transfer"],
  data: null,
  createdAt: "2024-01-01T00:00:00Z",
};

const emptyPage: PaginatedEvents = { events: [], cursor: null, hasMore: false };
const onePage: PaginatedEvents = { events: [sampleEvent], cursor: "cur1", hasMore: false };

function wrapper({ children }: { children: React.ReactNode }) {
  return (
    <TridentProvider apiUrl="http://localhost:3000" apiKey="test-key">
      {children}
    </TridentProvider>
  );
}

// ---------------------------------------------------------------------------
// useContractEvents
// ---------------------------------------------------------------------------

describe("useContractEvents", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("starts in loading state", () => {
    mockQueryEvents.mockReturnValue(new Promise(() => {})); // never resolves
    const { result } = renderHook(() => useContractEvents({}), { wrapper });
    expect(result.current.isLoading).toBe(true);
    expect(result.current.events).toEqual([]);
    expect(result.current.error).toBeNull();
  });

  it("returns events on success", async () => {
    mockQueryEvents.mockResolvedValue(onePage);
    const { result } = renderHook(() => useContractEvents({ contractId: "CTEST" }), { wrapper });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.events).toHaveLength(1);
    expect(result.current.events[0].id).toBe("evt-1");
    expect(result.current.cursor).toBe("cur1");
    expect(result.current.error).toBeNull();
  });

  it("sets error state when fetch fails", async () => {
    mockQueryEvents.mockRejectedValue(new Error("network failure"));
    const { result } = renderHook(() => useContractEvents({}), { wrapper });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.error).toBeInstanceOf(Error);
    expect(result.current.error?.message).toBe("network failure");
    expect(result.current.events).toEqual([]);
  });

  it("re-fetches when refresh() is called", async () => {
    mockQueryEvents.mockResolvedValueOnce(emptyPage).mockResolvedValueOnce(onePage);
    const { result } = renderHook(() => useContractEvents({}), { wrapper });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.events).toHaveLength(0);

    act(() => { result.current.refresh(); });

    await waitFor(() => expect(result.current.events).toHaveLength(1));
    expect(mockQueryEvents).toHaveBeenCalledTimes(2);
  });

  it("passes contractId and limit to queryEvents", async () => {
    mockQueryEvents.mockResolvedValue(emptyPage);
    renderHook(() => useContractEvents({ contractId: "CTEST", limit: 5 }), { wrapper });

    await waitFor(() => expect(mockQueryEvents).toHaveBeenCalled());
    expect(mockQueryEvents).toHaveBeenCalledWith(
      expect.objectContaining({ contractId: "CTEST", limit: 5 }),
    );
  });

  it("does not pass refreshInterval to queryEvents", async () => {
    mockQueryEvents.mockResolvedValue(emptyPage);
    renderHook(() => useContractEvents({ refreshInterval: 5000 }), { wrapper });

    await waitFor(() => expect(mockQueryEvents).toHaveBeenCalled());
    expect(mockQueryEvents).toHaveBeenCalledWith(
      expect.not.objectContaining({ refreshInterval: expect.anything() }),
    );
  });
});

// ---------------------------------------------------------------------------
// useSubscription
// ---------------------------------------------------------------------------

describe("useSubscription", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("calls subscribeToContract on mount and unsubscribes on unmount", () => {
    const unsubscribe = vi.fn();
    mockSubscribeToContract.mockReturnValue({ unsubscribe });

    const { unmount } = renderHook(
      () => useSubscription({ contractId: "CTEST" }),
      { wrapper },
    );

    expect(mockSubscribeToContract).toHaveBeenCalledOnce();
    expect(mockSubscribeToContract).toHaveBeenCalledWith(
      expect.objectContaining({ contractId: "CTEST" }),
    );

    unmount();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("starts with lastEvent null and isConnected true", () => {
    mockSubscribeToContract.mockReturnValue({ unsubscribe: vi.fn() });
    const { result } = renderHook(
      () => useSubscription({ contractId: "CTEST" }),
      { wrapper },
    );
    expect(result.current.lastEvent).toBeNull();
    expect(result.current.isConnected).toBe(true);
  });

  it("updates lastEvent when onEvent is called", () => {
    let capturedOnEvent: ((e: SorobanEvent) => void) | undefined;
    mockSubscribeToContract.mockImplementation(
      (p: { onEvent: (e: SorobanEvent) => void }) => {
        capturedOnEvent = p.onEvent;
        return { unsubscribe: vi.fn() };
      },
    );

    const { result } = renderHook(
      () => useSubscription({ contractId: "CTEST" }),
      { wrapper },
    );

    act(() => { capturedOnEvent?.(sampleEvent); });

    expect(result.current.lastEvent).toEqual(sampleEvent);
  });

  it("calls the user-supplied onEvent callback", () => {
    const userOnEvent = vi.fn();
    let capturedOnEvent: ((e: SorobanEvent) => void) | undefined;
    mockSubscribeToContract.mockImplementation(
      (p: { onEvent: (e: SorobanEvent) => void }) => {
        capturedOnEvent = p.onEvent;
        return { unsubscribe: vi.fn() };
      },
    );

    renderHook(
      () => useSubscription({ contractId: "CTEST", onEvent: userOnEvent }),
      { wrapper },
    );

    act(() => { capturedOnEvent?.(sampleEvent); });
    expect(userOnEvent).toHaveBeenCalledWith(sampleEvent);
  });

  it("does not leak memory — no re-subscription when callbacks change", () => {
    const unsubscribe = vi.fn();
    mockSubscribeToContract.mockReturnValue({ unsubscribe });

    const { rerender } = renderHook(
      ({ cb }: { cb: () => void }) =>
        useSubscription({ contractId: "CTEST", onEvent: cb }),
      {
        wrapper,
        initialProps: { cb: vi.fn() },
      },
    );

    rerender({ cb: vi.fn() }); // new callback reference
    // subscribeToContract must NOT be called again — only once on mount
    expect(mockSubscribeToContract).toHaveBeenCalledOnce();
    expect(unsubscribe).not.toHaveBeenCalled();
  });

  it("re-subscribes when contractId changes", () => {
    const unsubscribe = vi.fn();
    mockSubscribeToContract.mockReturnValue({ unsubscribe });

    const { rerender } = renderHook(
      ({ id }: { id: string }) => useSubscription({ contractId: id }),
      { wrapper, initialProps: { id: "C1" } },
    );

    rerender({ id: "C2" });

    expect(unsubscribe).toHaveBeenCalledOnce();
    expect(mockSubscribeToContract).toHaveBeenCalledTimes(2);
    expect(mockSubscribeToContract).toHaveBeenLastCalledWith(
      expect.objectContaining({ contractId: "C2" }),
    );
  });

  // Note: checking isConnected===false post-unmount is unreliable in React 18+
  // because state updates in cleanup functions are discarded after unmount.
  // The subscription cleanup (unsubscribe call) is covered by the
  // "calls subscribeToContract on mount and unsubscribes on unmount" test above.
});
