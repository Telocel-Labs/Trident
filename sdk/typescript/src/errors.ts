export type TridentErrorCode =
  | "NOT_FOUND"
  | "UNAUTHORIZED"
  | "RATE_LIMITED"
  | "INTERNAL";

export class TridentError extends Error {
  readonly code: TridentErrorCode;
  readonly cause?: unknown;

  constructor(code: TridentErrorCode, message: string, cause?: unknown) {
    super(message);
    this.name = "TridentError";
    this.code = code;
    this.cause = cause;
  }
}

/** Map an HTTP status code to a TridentError. */
export function httpStatusToError(
  status: number,
  body: string,
): TridentError {
  switch (status) {
    case 401:
      return new TridentError("UNAUTHORIZED", body || "Unauthorized");
    case 404:
      return new TridentError("NOT_FOUND", body || "Not found");
    case 429:
      return new TridentError("RATE_LIMITED", body || "Rate limit exceeded");
    default:
      return new TridentError("INTERNAL", body || `HTTP ${status}`);
  }
}
