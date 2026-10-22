import type { APIRoute } from 'astro';
import { getIndexerStats } from '../../lib/api';
import type { Network } from '../../lib/types';
import { serverCache } from '../../lib/cache';

export const GET: APIRoute = async ({ url }) => {
  const rawNetwork = url.searchParams.get('network');
  const network: Network = rawNetwork === 'mainnet' ? 'mainnet' : 'testnet';
  const cacheKey = `status:${network}`;

  try {
    const { data: stats, cached } = await serverCache.getOrSet(cacheKey, 3000, async () => {
      return await getIndexerStats(network);
    });

    return new Response(JSON.stringify(stats), {
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'public, max-age=3, s-maxage=3',
        'X-Cache': cached ? 'HIT' : 'MISS',
      },
    });
  } catch {
    // Covers both the documented 503 and an unreachable API — the page shows
    // "indexer unreachable" either way.
    return new Response(JSON.stringify({ error: 'indexer unreachable' }), {
      status: 503,
      headers: { 'Content-Type': 'application/json' },
    });
  }
};
