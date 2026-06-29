import type { APIRoute } from 'astro';
import { listEvents } from '../../lib/api';
import type { Network } from '../../lib/types';

export const GET: APIRoute = async ({ url }) => {
  const rawNetwork = url.searchParams.get('network');
  const network: Network = rawNetwork === 'mainnet' ? 'mainnet' : 'testnet';
  const contractId = url.searchParams.get('contractId') ?? undefined;
  const topic0 = url.searchParams.get('topic0') ?? undefined;
  const cursor = url.searchParams.get('cursor') ?? undefined;
  const rawFrom = url.searchParams.get('ledgerFrom');
  const rawTo = url.searchParams.get('ledgerTo');

  try {
    const result = await listEvents({
      network,
      contractId,
      topic0,
      cursor,
      ledgerFrom: rawFrom ? Number(rawFrom) : undefined,
      ledgerTo: rawTo ? Number(rawTo) : undefined,
      limit: 25,
    });
    return new Response(JSON.stringify(result), {
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'no-store',
      },
    });
  } catch (err) {
    return new Response(JSON.stringify({ error: 'Failed to fetch events' }), {
      status: 502,
      headers: { 'Content-Type': 'application/json' },
    });
  }
};
