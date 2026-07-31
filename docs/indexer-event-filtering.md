# Server-side event filtering

How the indexer narrows `getEvents` at the RPC instead of after the fact
(issue #203).

## Behaviour

The contract allowlist in `indexed_contracts` is compiled into the `filters`
array of every `getEvents` request:

```json
{
  "filters": [
    { "type": "contract", "contractIds": ["CAAA...", "CBBB..."] }
  ]
}
```

The RPC then never sends events we would discard, which saves bandwidth, RPC
quota, and the CPU cost of XDR-decoding payloads destined for the bin.

The client-side allowlist check in the streamer stays. It is a no-op whenever
server filtering is active, and it is the correctness boundary when filtering is
degraded (below) or when an RPC ignores the filter.

## Modes

| Allowlist size            | Request                                    |
| ------------------------- | ------------------------------------------ |
| empty                     | no filters — index everything              |
| 1–5 contracts             | one filter                                 |
| 6–25 contracts            | sharded into up to 5 filters of 5 IDs each |
| more than 25 contracts    | no filters, warning logged — index-all     |

The caps come from the Soroban RPC: at most 5 filters per request and 5 contract
IDs per filter. Past 25 contracts the allowlist cannot be expressed, so the
indexer degrades to index-all rather than silently dropping contracts from the
filter — correct, just less efficient. The warning names the count so the cause
is visible in logs.

Contract IDs are sorted before sharding, so the request body is deterministic for
a given allowlist.

## Topic narrowing

`INDEX_TOPIC_FILTERS` adds topic patterns to each filter. It is off by default.

```
INDEX_TOPIC_FILTERS=transfer/*/*,mint/*/*
```

Each pattern is a `/`-separated segment list. A segment is either a Soroban
symbol — XDR-encoded and base64'd to match what the RPC compares against — or a
wildcard: `*` for one topic position, `**` for the remainder.

The segment count must line up with the events you want. `transfer` alone matches
only single-topic events; a SEP-41 transfer carries three topics, so it needs
`transfer/*/*`. Getting this wrong silently indexes nothing, which is why an
unparseable spec fails startup rather than falling back to no filter.

Topic patterns apply only alongside a contract allowlist. An empty allowlist
always means index-all; contract-agnostic topic-only filtering is out of scope.

## Verifying

The streamer tests assert on the outbound request body itself: the mock only
matches when the expected filter block is present, so a dropped or malformed
filter fails the poll instead of passing quietly.
