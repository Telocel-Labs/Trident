# Trident Soroban Event Explorer

A web explorer for Soroban contract events on the Stellar network, powered by the Trident indexer.

## Features

- **Real-time Event Stream**: Live SSE connection to the indexer for real-time contract events.
- **Contract Search**: Search by contract ID or view recent events across the network.
- **Pagination & Filters**: Paginate through historical events with optional topic filters.
- **Responsive Design**: Clean, accessible UI built with Astro, React, and Tailwind CSS.
- **Traffic Isolation & Caching**: Server-side in-memory response caching and upstream SSE multiplexing ensure smooth operation under heavy public traffic without exhausting API quotas (issue #784).

## Configuration

Set the following environment variables in `.env`:

| Variable | Required | Description |
|----------|----------|-------------|
| `TRIDENT_TESTNET_API_URL` | Yes | Base URL for the testnet Trident REST API |
| `TRIDENT_MAINNET_API_URL` | Yes | Base URL for the mainnet Trident REST API |
| `EXPLORER_API_KEY` | Yes | Internal API key (`internal` tier, sized for public explorer traffic) |
| `EXPLORER_EDGE_RATE_LIMIT_RPM` | No | Edge rate limit per visitor IP in requests/min (default: 120) |
| `TRIDENT_TESTNET_SOROBAN_RPC_URL` | No | Soroban RPC used to probe contracts on-chain (testnet default: `https://soroban-testnet.stellar.org`) |
| `TRIDENT_MAINNET_SOROBAN_RPC_URL` | No | Soroban RPC used to probe contracts on-chain (mainnet default: `https://mainnet.sorobanrpc.com`) |

The `EXPLORER_API_KEY` is used server-side only and is never sent to the browser.

## Result states

The explorer distinguishes failures deliberately instead of showing blank pages or raw errors:

- **Loading**: the homepage ticker shows a content skeleton while the recent-events feed loads, never an empty wait.
- **No events yet**: the contract is quiet (and being watched live), so nothing is missed.
- **Not indexed yet**: the contract is emitting on-chain events but Trident hasn't indexed them — shown only after a best-effort Soroban RPC probe confirms the contract is live.
- **Invalid contract**: the searched address fails the Stellar strkey format + checksum check, answered locally in milliseconds.
- **Not found**: the event or contract isn't in the index (e.g. rotated out of retention).
- **Indexer unavailable**: any upstream failure maps to a human-readable reason (`network`, `rate_limited`, `unauthorized`, `timeout`, `down`) with a retry path.
- **Live feed status**: the contract page shows a persistent connection pill (connecting / live / reconnecting / off) and auto-resumes the SSE stream via `Last-Event-ID`, so no events are skipped during a drop.

## Rate Limiting & Traffic Protection

- **API Tier**: The explorer server key `EXPLORER_API_KEY` is configured on the `internal` rate limit tier (up to 1,000 RPS).
- **Server Response Cache**: Endpoints (`/api/status.json`, `/api/recent-events.json`, `/api/events.json`) use an in-memory TTL cache (3–5s) with singleflight deduplication to avoid redundant upstream hits under concurrency.
- **SSE Multiplexing**: Multiple visitors viewing live feeds for the same contract share a single upstream EventSource connection via `SSEHub`.
- **Edge Visitor Limiting**: Built-in middleware enforces per-IP limits (`EXPLORER_EDGE_RATE_LIMIT_RPM`, default 120 req/min) returning HTTP 429 directly at the edge.

## Deployment

The Astro SSR app runs as a Node.js standalone server. Use Vercel, Railway, or Fly.io with `npm run build && node dist/server/entry.mjs`.
