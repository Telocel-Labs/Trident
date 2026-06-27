# OpenAPI Specification

This directory contains the OpenAPI 3.1 specification for the Trident REST API.

## Single Source of Truth

The `openapi.yaml` file is the canonical contract for the REST API. All endpoint documentation, request/response schemas, and error codes are defined here.

## Generating SDK Types

The TypeScript SDK types are auto-generated from this spec:

```bash
make generate-sdk-types
```

This runs:

```bash
cd sdk/typescript && npm run generate:types
```

The generated file `sdk/typescript/src/api-types.gen.ts` is committed to the repository and kept up-to-date by CI.

## Linting the Spec

Validate the spec against OpenAPI 3.1 best practices:

```bash
make lint-openapi
```

This uses [Spectral](https://stoplight.io/open-source/spectral) to catch:

- Missing operationId on endpoints
- Missing descriptions on parameters
- Response schemas with no content
- Invalid property types
- And more OpenAPI spec violations

Install Spectral globally if needed:

```bash
npm install -g @stoplight/spectral-cli
```

## CI Checks

The GitHub Actions workflow (`../.github/workflows/ci.yml`) runs:

1. **Spectral lint** — Fails if the spec has any errors
2. **SDK type generation** — Fails if `api-types.gen.ts` is stale (not committed after spec changes)

## Endpoints Documented

- `GET /v1/health` — Health check
- `GET /v1/events` — List events with filtering and pagination
- `GET /v1/events/{id}` — Fetch single event by UUID
- `GET /v1/events/stream` — Real-time event streaming (Server-Sent Events)
- `GET /v1/stats/contracts` — Contract activity analytics
- `GET /v1/admin/db` — Admin: database stats

## Making Changes

1. **Update `openapi.yaml`** with new endpoints, parameters, or schemas
2. **Run `make lint-openapi`** to validate locally
3. **Run `make generate-sdk-types`** to update SDK types
4. **Commit both files** (`openapi.yaml` and `sdk/typescript/src/api-types.gen.ts`)
5. Push to PR — CI will validate both the spec and the generated types

## References

- [OpenAPI 3.1 Specification](https://spec.openapis.org/oas/v3.1.0)
- [Spectral Rulesets](https://docs.stoplight.io/spectral/reference/openapi-rules)
- [openapi-typescript](https://github.com/drwpow/openapi-typescript) — TypeScript type generator
