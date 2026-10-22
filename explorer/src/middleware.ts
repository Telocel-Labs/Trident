/**
 * Edge Rate Limiting Middleware for Trident Explorer.
 *
 * Implements per-visitor sliding window rate limiting at the explorer edge (issue #784)
 * to prevent individual rogue visitors, bots or scrapers from exhausting server resources
 * or the upstream API key quota.
 */

import type { MiddlewareHandler } from 'astro';

interface RateLimitEntry {
  count: number;
  resetAt: number;
}

const visitorLimits = new Map<string, RateLimitEntry>();
const WINDOW_MS = 60 * 1000; // 1 minute window
const DEFAULT_MAX_REQUESTS_PER_MIN = 120; // 120 requests/min per IP

export function getClientIP(request: Request): string {
  const forwarded = request.headers.get('x-forwarded-for');
  if (forwarded) {
    return forwarded.split(',')[0].trim();
  }
  return request.headers.get('x-real-ip') || '127.0.0.1';
}

export function resetVisitorLimits(): void {
  visitorLimits.clear();
}

export const onRequest: MiddlewareHandler = async ({ request, url }, next) => {
  // Only enforce edge rate-limiting on API routes (/api/*)
  if (url.pathname.startsWith('/api/')) {
    const ip = getClientIP(request);
    const now = Date.now();
    let entry = visitorLimits.get(ip);

    if (!entry || entry.resetAt <= now) {
      entry = { count: 1, resetAt: now + WINDOW_MS };
      visitorLimits.set(ip, entry);
    } else {
      entry.count++;
    }

    const limit = Number(
      process.env.EXPLORER_EDGE_RATE_LIMIT_RPM || DEFAULT_MAX_REQUESTS_PER_MIN
    );
    const remaining = Math.max(0, limit - entry.count);
    const resetSec = Math.ceil((entry.resetAt - now) / 1000);

    if (entry.count > limit) {
      return new Response(
        JSON.stringify({
          error: 'Too Many Requests',
          message: 'Explorer edge rate limit exceeded. Please slow down.',
        }),
        {
          status: 429,
          headers: {
            'Content-Type': 'application/json',
            'Retry-After': String(resetSec),
            'X-RateLimit-Limit': String(limit),
            'X-RateLimit-Remaining': '0',
            'X-RateLimit-Reset': String(resetSec),
          },
        }
      );
    }

    const response = await next();
    response.headers.set('X-RateLimit-Limit', String(limit));
    response.headers.set('X-RateLimit-Remaining', String(remaining));
    response.headers.set('X-RateLimit-Reset', String(resetSec));
    return response;
  }

  return next();
};
