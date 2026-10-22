/**
 * Upstream SSE Multiplexer / Broadcast Hub for Trident Explorer.
 *
 * Rather than opening a separate upstream SSE stream per browser visitor (which
 * rapidly exhausts the explorer's per-key stream quota and API limits), this hub
 * shares ONE upstream SSE connection per (network, contractId, topic0) across
 * any number of concurrent visitors (issue #784).
 */

export interface Subscriber {
  id: string;
  write: (chunk: Uint8Array) => void;
  close: () => void;
}

interface Channel {
  key: string;
  subscribers: Map<string, Subscriber>;
  abortController: AbortController;
  connecting: boolean;
  upstreamConnected: boolean;
}

export class SSEHub {
  private channels = new Map<string, Channel>();
  private upstreamCallCount = 0;
  private fetchFn: typeof fetch;
  private getApiUrl: (network: string) => string;
  private getApiKey: () => string;

  constructor(
    fetchFn: typeof fetch = fetch,
    getApiUrl: (network: string) => string = (n) =>
      n === 'mainnet' ? 'https://api.mainnet.trident.dev' : 'https://api.testnet.trident.dev',
    getApiKey: () => string = () => ''
  ) {
    this.fetchFn = fetchFn;
    this.getApiUrl = getApiUrl;
    this.getApiKey = getApiKey;
  }

  channelKey(network: string, contractId: string, topic0: string = ''): string {
    return `${network}:${contractId}:${topic0}`;
  }

  getUpstreamCallCount(): number {
    return this.upstreamCallCount;
  }

  getActiveChannelCount(): number {
    return this.channels.size;
  }

  getTotalSubscriberCount(): number {
    let total = 0;
    for (const ch of this.channels.values()) {
      total += ch.subscribers.size;
    }
    return total;
  }

  /**
   * Subscribe a client to a contract's event stream.
   * Returns an unsubscribe function.
   */
  subscribe(
    network: string,
    contractId: string,
    topic0: string,
    subscriber: Subscriber
  ): () => void {
    const key = this.channelKey(network, contractId, topic0);
    let channel = this.channels.get(key);

    if (!channel) {
      const abortController = new AbortController();
      channel = {
        key,
        subscribers: new Map(),
        abortController,
        connecting: false,
        upstreamConnected: false,
      };
      this.channels.set(key, channel);
    }

    channel.subscribers.set(subscriber.id, subscriber);

    // If upstream is not yet connected or connecting, start it
    if (!channel.upstreamConnected && !channel.connecting) {
      this.startUpstream(channel, network, contractId, topic0);
    }

    // Return unsubscriber
    return () => {
      this.unsubscribe(key, subscriber.id);
    };
  }

  private unsubscribe(key: string, subscriberId: string): void {
    const channel = this.channels.get(key);
    if (!channel) return;

    channel.subscribers.delete(subscriberId);
    if (channel.subscribers.size === 0) {
      // No more subscribers: close upstream connection to save resources
      channel.abortController.abort();
      this.channels.delete(key);
    }
  }

  private async startUpstream(
    channel: Channel,
    network: string,
    contractId: string,
    topic0: string
  ): Promise<void> {
    channel.connecting = true;
    this.upstreamCallCount++;

    const base = this.getApiUrl(network);
    const url = new URL(`${base}/v1/events/stream`);
    url.searchParams.set('contract_id', contractId);
    if (topic0) url.searchParams.set('topic_0', topic0);

    const headers: Record<string, string> = {
      Accept: 'text/event-stream',
    };
    const key = this.getApiKey();
    if (key) headers['X-API-Key'] = key;

    try {
      const res = await this.fetchFn(url.toString(), {
        headers,
        signal: channel.abortController.signal,
      });

      if (!res.ok || !res.body) {
        throw new Error(`Upstream stream returned HTTP ${res.status}`);
      }

      channel.connecting = false;
      channel.upstreamConnected = true;

      const reader = res.body.getReader();
      while (!channel.abortController.signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value) {
          // Broadcast received SSE chunk to all active subscribers
          for (const sub of channel.subscribers.values()) {
            try {
              sub.write(value);
            } catch (_) {
              // Ignore individual subscriber write errors
            }
          }
        }
      }
    } catch (err: any) {
      if (!channel.abortController.signal.aborted) {
        console.warn(`[SSEHub] Upstream disconnected for ${channel.key}: ${err.message}`);
      }
    } finally {
      channel.connecting = false;
      channel.upstreamConnected = false;
      // Close remaining subscribers if upstream closed unexpectedly
      for (const sub of channel.subscribers.values()) {
        try {
          sub.close();
        } catch (_) {}
      }
      this.channels.delete(channel.key);
    }
  }

  clear(): void {
    for (const ch of this.channels.values()) {
      ch.abortController.abort();
    }
    this.channels.clear();
    this.upstreamCallCount = 0;
  }
}

export const sseHub = new SSEHub(
  fetch,
  (n) => (n === 'mainnet' ? (import.meta.env.TRIDENT_MAINNET_API_URL ?? 'https://api.mainnet.trident.dev') : (import.meta.env.TRIDENT_TESTNET_API_URL ?? 'https://api.testnet.trident.dev')),
  () => import.meta.env.EXPLORER_API_KEY ?? ''
);
