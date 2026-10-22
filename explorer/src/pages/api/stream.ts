import type { APIRoute } from 'astro';
import type { Network } from '../../lib/types';
import { sseHub } from '../../lib/sse-hub';

const CONTRACT_ID_RE = /^C[A-Z2-7]{55}$/;

export const GET: APIRoute = async ({ url, request }) => {
  const rawNetwork = url.searchParams.get('network');
  const network: Network = rawNetwork === 'mainnet' ? 'mainnet' : 'testnet';
  const contractId = url.searchParams.get('contractId') ?? '';
  const topic0 = url.searchParams.get('topic0') ?? '';

  if (!CONTRACT_ID_RE.test(contractId)) {
    return new Response(JSON.stringify({ error: 'invalid contractId' }), {
      status: 400,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  // Create client-facing streaming response multiplexed via sseHub (issue #784).
  // All concurrent visitors viewing this contract share one single upstream
  // SSE connection, preventing rate limiting (429) and stream quota exhaustion.
  const stream = new ReadableStream({
    start(controller) {
      const subscriberId = Math.random().toString(36).slice(2);
      const unsubscribe = sseHub.subscribe(network, contractId, topic0, {
        id: subscriberId,
        write(chunk) {
          try {
            controller.enqueue(chunk);
          } catch (_) {
            unsubscribe();
          }
        },
        close() {
          try {
            controller.close();
          } catch (_) {}
        },
      });

      request.signal.addEventListener('abort', () => {
        unsubscribe();
      });
    },
  });

  return new Response(stream, {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-store',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    },
  });
};
