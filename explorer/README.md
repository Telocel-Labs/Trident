# Trident Explorer

Public event explorer for Soroban contracts on Stellar. Read-only, no API key required for end users.

## Stack

- [Astro](https://astro.build) (SSR, `@astrojs/node` standalone adapter)
- Tailwind CSS
- TypeScript

## Routes

| Path | Description |
|------|-------------|
| `/` | Landing page — search + live recent events ticker |
| `/contract/:address` | All events for a contract, paginated, server-rendered |
| `/contract/:address/event/:id` | Single event detail, shareable, og:tags |

## Setup

```bash
cp .env.example .env
# edit .env with your API URLs and internal key
npm install
npm run dev        # http://localhost:4321
npm run build      # production build
npm run preview    # preview production build
npm run lint       # type-check with astro check
```

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TRIDENT_TESTNET_API_URL` | Yes | Base URL for the testnet Trident REST API |
| `TRIDENT_MAINNET_API_URL` | Yes | Base URL for the mainnet Trident REST API |
| `EXPLORER_API_KEY` | Yes | Internal API key (free tier, created at deploy time) |

The `EXPLORER_API_KEY` is used server-side only and is never sent to the browser.

## Rate limiting

- The explorer uses an internal `EXPLORER_API_KEY` at the free tier (60 req/min).
- IP-based rate limiting (30 req/min per IP) must be configured at the CDN/edge layer:
  - **Vercel**: Edge Config rate limiting rule
  - **Cloudflare**: Rate limiting rule on the `explorer.*` hostname

## Deployment

The Astro SSR app runs as a Node.js standalone server. A `Dockerfile` is not included — use Vercel, Railway, or Fly.io with `npm run build && node dist/server/entry.mjs`.
