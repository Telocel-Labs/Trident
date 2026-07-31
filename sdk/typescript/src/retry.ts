/**
 * Retry policy for idempotent HTTP requests (GET). Honours `Retry-After` on
 * 429/503 responses and falls back to exponential backoff with jitter
 * otherwise. Pass `false` in place of a `RetryConfig` to disable retries
 * entirely for a client or a single call.
 */
export interface RetryConfig {
  /** Total number of attempts, including the first. Defaults to 3. */
  maxAttempts?: number;
  /** Base delay in ms used for exponential backoff. Defaults to 100. */
  baseDelayMs?: number;
  /** Upper bound for a single computed backoff delay. Defaults to 2000. */
  maxDelayMs?: number;
  /** Upper bound on total time spent waiting across all retries (including
   * any honoured `Retry-After`). Defaults to 10000. */
  maxTotalWaitMs?: number;
  /** Randomize each computed delay in [0, delay). Defaults to true. */
  jitter?: boolean;
}

export const DEFAULT_RETRY_CONFIG: Required<RetryConfig> = {
  maxAttempts: 3,
  baseDelayMs: 100,
  maxDelayMs: 2_000,
  maxTotalWaitMs: 10_000,
  jitter: true,
};

/**
 * Merge a per-call override (if any) with the client-level config, falling
 * back to defaults. `false` at either level disables retries.
 */
export function resolveRetryConfig(
  override: RetryConfig | false | undefined,
  base: RetryConfig | false | undefined,
): Required<RetryConfig> | null {
  const cfg = override !== undefined ? override : base;
  if (cfg === false) {
    return null;
  }
  return { ...DEFAULT_RETRY_CONFIG, ...cfg };
}

/** Only 429 (rate limited) and 503 (service unavailable) are retried. */
export function isRetryableStatus(status: number): boolean {
  return status === 429 || status === 503;
}

/**
 * Parse a `Retry-After` header value, which per RFC 9110 is either a number
 * of seconds or an HTTP date. Returns `null` when absent or unparseable.
 */
export function parseRetryAfterMs(
  headerValue: string | null | undefined,
): number | null {
  if (!headerValue) {
    return null;
  }
  const trimmed = headerValue.trim();
  if (trimmed === "") {
    return null;
  }
  const seconds = Number(trimmed);
  if (Number.isFinite(seconds)) {
    return Math.max(0, seconds * 1000);
  }
  const dateMs = Date.parse(trimmed);
  if (!Number.isNaN(dateMs)) {
    return Math.max(0, dateMs - Date.now());
  }
  return null;
}

/** Exponential backoff with optional full jitter, capped at `maxDelayMs`. */
export function computeBackoffMs(
  attempt: number,
  cfg: Required<RetryConfig>,
): number {
  const exp = cfg.baseDelayMs * 2 ** (attempt - 1);
  const capped = Math.min(exp, cfg.maxDelayMs);
  return cfg.jitter ? Math.random() * capped : capped;
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
