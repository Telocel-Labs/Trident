# Token event fixtures

Golden inputs for the SEP-41 / Stellar-Asset-Contract event decoder
(`crates/indexer/src/parser/token_events.rs`, issue #211).

Each file holds one event exactly as the Soroban `getEvents` RPC delivers it —
base64-encoded XDR `ScVal` topics and body — plus the named fields the decoder
is expected to produce.

The payloads are XDR-encoded from the SEP-41 event layout rather than captured
from a live testnet node, so the account addresses are synthetic. The wire
encoding is real: the decoder reads these through the same base64 + XDR path it
uses in production, so a change to the layout or to `stellar-xdr` breaks these
tests.

To add a case, encode the topics and body as `ScVal` XDR, base64 them, and
record the expected decode in the same shape as the existing files.
