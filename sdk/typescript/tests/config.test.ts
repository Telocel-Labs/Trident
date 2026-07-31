import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { TridentClient, TridentError } from "../src/index.js";

const ORIGINAL_ENV = { ...process.env };

describe("config precedence", () => {
  beforeEach(() => {
    process.env.TRIDENT_API_KEY = "env-key";
    process.env.TRIDENT_BASE_URL = "https://env.example.com";
  });

  afterEach(() => {
    process.env = { ...ORIGINAL_ENV };
  });

  it("prefers explicit apiKey/apiUrl over env vars", () => {
    const client = new TridentClient({
      apiUrl: "https://explicit.example.com",
      apiKey: "explicit-key",
      network: "testnet",
    });

    expect(client.toString()).toContain("https://explicit.example.com");
    expect(client.toString()).not.toContain("explicit-key");
  });

  it("falls back to env vars when config omits apiKey/apiUrl", () => {
    const client = new TridentClient({ network: "testnet" });

    expect(client.toString()).toContain("https://env.example.com");
  });

  it("throws a clear CONFIG error when apiKey is missing everywhere", () => {
    delete process.env.TRIDENT_API_KEY;

    expect(() => new TridentClient({ apiUrl: "https://x.example.com", network: "testnet" })).toThrow(
      TridentError,
    );
    try {
      new TridentClient({ apiUrl: "https://x.example.com", network: "testnet" });
    } catch (err) {
      expect(err).toBeInstanceOf(TridentError);
      expect((err as TridentError).code).toBe("CONFIG");
    }
  });

  it("throws a clear CONFIG error when apiUrl is missing everywhere", () => {
    delete process.env.TRIDENT_BASE_URL;

    expect(() => new TridentClient({ apiKey: "explicit-key", network: "testnet" })).toThrow(
      TridentError,
    );
  });
});

describe("redaction", () => {
  beforeEach(() => {
    delete process.env.TRIDENT_API_KEY;
    delete process.env.TRIDENT_BASE_URL;
  });

  it("never includes the raw API key in toString()", () => {
    const client = new TridentClient({
      apiUrl: "https://x.example.com",
      apiKey: "super-secret-value",
      network: "testnet",
    });

    const repr = client.toString();
    expect(repr).not.toContain("super-secret-value");
    expect(repr).toContain("***");
  });
});
