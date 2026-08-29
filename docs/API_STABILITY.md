# `/v1/` API stability and versioning policy

This document states what `/v1/` promises before launch, so a change to it
is a deliberate decision rather than an accident someone downstream has to
absorb.

## What `/v1/` promises

Once a `/v1/` endpoint is not marked `experimental` (see below), Trident
commits to:

- The request shape (path, required parameters, request body schema) will
  not change in a way that breaks an existing valid request.
- The response shape will not remove a field, change a field's type, or
  change the meaning of an existing field's value.
- The endpoint will not be removed without going through the deprecation
  process below.

## What may change without a version bump

- Adding a new optional request parameter.
- Adding a new field to a response object.
- Adding a new endpoint.
- Performance, rate limits, and internal implementation details not
  observable in the request/response contract.
- Bug fixes that make a response match its documented schema (a field that
  was documented as a string but sometimes returned `null` is a bug fix,
  not a breaking change).

## What requires a version bump (a new `/v2/` surface)

- Removing or renaming a field, parameter, or endpoint.
- Changing a field's type or its semantic meaning.
- Changing default behavior in a way that changes the response for
  existing callers who didn't opt in.
- Tightening validation such that a previously-accepted request is now
  rejected.

## Experimental endpoints

An endpoint may be marked `experimental` in the OpenAPI spec (via an
`x-experimental: true` extension) and in every SDK's generated docs. An
experimental endpoint carries none of the stability guarantees above and
may change or be removed without notice. Endpoints should only stay
experimental for a bounded period — either graduate to stable or be
removed.

**Before this freeze takes effect**, `/v1/admin/db` needs an explicit
decision from the API owner: it reads as an internal/operational
endpoint rather than a public contract, and should likely either be
marked `experimental`, moved off the public `/v1/` surface, or explicitly
confirmed as a supported public endpoint. Not resolved in this pass —
flagging it here rather than deciding unilaterally.

## Deprecation mechanism

1. Announce the deprecation in `CHANGELOG.md` with the planned removal
   date (minimum 90 days out for a stable endpoint).
2. From the announcement onward, every response from the deprecated
   endpoint includes:
   ```
   Deprecation: true
   Sunset: <RFC 3339 removal date>
   Link: <URL to migration guide>; rel="deprecation"
   ```
3. On the sunset date, the endpoint is removed (or, for a field-level
   deprecation, the field is removed) and the removal is noted in
   `CHANGELOG.md`.

## Where this lives

This policy should be linked from the top of `api/openapi.yaml`'s
description and from the published API docs, so a reader of either finds
it without having to know to look in this repo's `docs/` folder.
