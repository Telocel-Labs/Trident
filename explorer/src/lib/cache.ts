/**
 * Server-side in-memory response cache for Trident Explorer.
 *
 * Caches hot server-side responses (indexer stats, recent events, contract listings)
 * to avoid overwhelming the upstream Trident API and exhausting the single explorer
 * API key quota under concurrent visitor traffic (issue #784).
 */

interface CacheEntry<T> {
  value: T;
  expiresAt: number;
}

export class ResponseCache {
  private store = new Map<string, CacheEntry<any>>();
  private inflight = new Map<string, Promise<any>>();
  private hits = 0;
  private misses = 0;
  private defaultTtlMs: number;

  constructor(defaultTtlMs: number = 3000) {
    this.defaultTtlMs = defaultTtlMs;
  }

  /**
   * Returns cached value if present and not expired, otherwise runs fetcher,
   * caches the result, and returns it. Uses single-flight deduplication to
   * ensure concurrent incoming requests for the same key share a single upstream fetch.
   */
  async getOrSet<T>(
    key: string,
    ttlMs: number = this.defaultTtlMs,
    fetcher: () => Promise<T>
  ): Promise<{ data: T; cached: boolean }> {
    const now = Date.now();
    const entry = this.store.get(key);

    if (entry && entry.expiresAt > now) {
      this.hits++;
      return { data: entry.value as T, cached: true };
    }

    // Check if there is an in-flight fetch for this key
    let promise = this.inflight.get(key) as Promise<T> | undefined;
    if (!promise) {
      this.misses++;
      promise = (async () => {
        try {
          const res = await fetcher();
          this.store.set(key, {
            value: res,
            expiresAt: Date.now() + ttlMs,
          });
          return res;
        } finally {
          this.inflight.delete(key);
        }
      })();
      this.inflight.set(key, promise);
    } else {
      this.hits++;
    }

    const data = await promise;
    return { data, cached: false };
  }

  get<T>(key: string): T | null {
    const entry = this.store.get(key);
    if (!entry) return null;
    if (entry.expiresAt <= Date.now()) {
      this.store.delete(key);
      return null;
    }
    this.hits++;
    return entry.value as T;
  }

  set<T>(key: string, value: T, ttlMs: number = this.defaultTtlMs): void {
    this.store.set(key, {
      value,
      expiresAt: Date.now() + ttlMs,
    });
  }

  clear(): void {
    this.store.clear();
    this.inflight.clear();
    this.hits = 0;
    this.misses = 0;
  }

  getStats(): { hits: number; misses: number; size: number } {
    return {
      hits: this.hits,
      misses: this.misses,
      size: this.store.size,
    };
  }
}

export const serverCache = new ResponseCache(3000);
