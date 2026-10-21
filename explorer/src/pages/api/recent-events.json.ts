import type { APIRoute } from "astro";
import { ApiError, listEvents } from "../../lib/api";
import type { Network, UnreachableReason } from "../../lib/types";
import { serverCache } from "../../lib/cache";

export interface RecentEventsResponse {
  status: "ok" | "api_unreachable";
  events: Awaited<ReturnType<typeof listEvents>>["events"];
  reason?: UnreachableReason;
  message?: string;
}

const jsonHeaders = {
  "Content-Type": "application/json",
  "Cache-Control": "public, max-age=3, s-maxage=3",
};

function json(body: unknown, status = 200, cached = false): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      ...jsonHeaders,
      "X-Cache": cached ? "HIT" : "MISS",
    },
  });
}

export const GET: APIRoute = async ({ url }) => {
  const rawNetwork = url.searchParams.get("network");
  const network: Network = rawNetwork === "mainnet" ? "mainnet" : "testnet";
  const cacheKey = `recent-events:${network}`;

  try {
    const { data: result, cached } = await serverCache.getOrSet(cacheKey, 3000, async () => {
      return await listEvents({ limit: 10, network });
    });

    return json({
      status: "ok",
      events: result.events,
    } satisfies RecentEventsResponse, 200, cached);
  } catch (err) {
    if (!(err instanceof ApiError)) {
      return json({
        status: "api_unreachable",
        events: [],
        reason: "network",
        message: "Could not reach the Trident indexer. Check your connection.",
      } satisfies RecentEventsResponse, 502);
    }

    const { status, code } = err;
    let reason: UnreachableReason = "down";
    if (status === 429 || code === "RATE_LIMITED") reason = "rate_limited";
    else if (status === 401 || status === 403 || code === "UNAUTHORIZED")
      reason = "unauthorized";
    else if (status === 0 || code === "NETWORK") reason = "network";

    const messages: Record<UnreachableReason, string> = {
      rate_limited:
        "You are browsing faster than the explorer is allowed to — rate limiting kicked in. It will recover on its own in a moment.",
      unauthorized:
        "The explorer's server key is not configured. This is on us, not you.",
      network:
        "Could not reach the Trident indexer. Please check your connection and retry.",
      timeout: "The Trident indexer took too long to answer. Please retry.",
      down: "The Trident indexer is temporarily unavailable. Please try again shortly.",
    };

    const httpStatus =
      reason === "rate_limited" ? 429 : reason === "unauthorized" ? status : 502;
    return json({
      status: "api_unreachable",
      events: [],
      reason,
      message: messages[reason] ?? "Something went wrong.",
    } satisfies RecentEventsResponse, httpStatus);
  }
};
