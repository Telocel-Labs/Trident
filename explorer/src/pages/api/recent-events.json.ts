import type { APIRoute } from 'astro';
import { listEvents } from '../../lib/api';
import type { Network } from '../../lib/types';

export const GET: APIRoute = async ({ url }) => {
  const rawNetwork = url.searchParams.get('network');
  const network: Network = rawNetwork === 'mainnet' ? 'mainnet' : 'testnet';
  try {
    const result = await listEvents({ limit: 10, network });
    return new Response(JSON.stringify(result.events), {
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'no-store',
      },
    });
  } catch {
    return new Response('[]', {
      headers: { 'Content-Type': 'application/json' },
    });
  }
};
