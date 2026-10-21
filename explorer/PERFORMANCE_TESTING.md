# Performance Testing & Monitoring

This document explains how to test and monitor explorer performance against realistic testnet data volumes.

## Quick Start

### Local Testing

Test against a live testnet API:

```bash
npm run perf-test -- \
  --api-url https://api.testnet.trident.dev \
  --api-key YOUR_API_KEY \
  --contract-id CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4
```

Record/update baseline metrics:

```bash
npm run perf-test -- --record-baseline
```

Exit code 0 = all budgets met and regression ≤ 10%. Exit code 1 = budget exceeded or regression detected.

### CI Testing

Performance tests run automatically:
- **Schedule**: Weekly, every Monday at 9 AM UTC
- **Trigger**: Manual via `workflow_dispatch` with custom API URL or baseline update flag
- **Budget**: Enforces absolute targets in `PERFORMANCE_BUDGET.md` and regression limits against `PERFORMANCE_BASELINE.json`

Failures notify through GitHub Step Summaries and create/comment on performance issues or pull requests.

## Files

| File | Purpose |
|------|---------|
| `PERFORMANCE_BUDGET.md` | Explicit targets (FCP, LCP, API latency) |
| `PERFORMANCE_BASELINE.json` | Committed baseline measurements |
| `SCALE_BOTTLENECKS.md` | Known issues and mitigation strategies |
| `scripts/perf-test.ts` | Node.js test suite with regression detection |
| `.github/workflows/explorer-perf.yml` | Weekly scheduled GitHub Actions workflow with notifications |
| `.lighthouserc.json` | Lighthouse CI config |
| `.lighthouse.config.js` | Lighthouse configuration presets (desktop, throttling) |

## Measurement

### What We Measure

1. **API Latency** (p50, p95)
   - Home ticker: `GET /v1/events?limit=10`
   - Contract list: `GET /v1/events?contractId=...&limit=25`
   - Event detail: `GET /v1/events/:id`

2. **Server Response Time** (Astro SSR)
   - Excludes network latency to API
   - Includes rendering + template processing

3. **Core Web Vitals** (Lighthouse CI)
   - FCP: First Contentful Paint
   - LCP: Largest Contentful Paint
   - CLS: Cumulative Layout Shift

### Budget & Regression Thresholds

All budgets assume **realistic testnet data** (10k+ events/contract, 100k+ total).
Any measurement exceeding the absolute budget limit or regressing >10% compared to `PERFORMANCE_BASELINE.json` triggers a failure.
