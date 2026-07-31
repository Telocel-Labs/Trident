import { TridentError } from "./errors.js";

/**
 * Precedence for both fields is: explicit constructor value > environment
 * variable. Neither value is ever logged verbatim — see {@link redactKey}.
 */
export const ENV_API_KEY = "TRIDENT_API_KEY";
export const ENV_BASE_URL = "TRIDENT_BASE_URL";

function readEnv(name: string): string | undefined {
  // `process` is undefined in browser bundles; guard so the SDK still works there.
  if (typeof process !== "undefined" && process.env) {
    return process.env[name];
  }
  return undefined;
}

export function resolveApiKey(apiKey: string | undefined): string {
  const resolved = apiKey || readEnv(ENV_API_KEY);
  if (!resolved) {
    throw new TridentError(
      "CONFIG",
      `Trident API key is required: pass apiKey in the client config or set the ${ENV_API_KEY} environment variable.`,
    );
  }
  return resolved;
}

export function resolveApiUrl(apiUrl: string | undefined): string {
  const resolved = apiUrl || readEnv(ENV_BASE_URL);
  if (!resolved) {
    throw new TridentError(
      "CONFIG",
      `Trident apiUrl is required: pass apiUrl in the client config or set the ${ENV_BASE_URL} environment variable.`,
    );
  }
  return resolved;
}

/** Returns a redacted form of an API key, safe to log or print. */
export function redactKey(key: string): string {
  if (!key) return "<empty>";
  if (key.length <= 4) return "***";
  return `***${key.slice(-4)}`;
}
