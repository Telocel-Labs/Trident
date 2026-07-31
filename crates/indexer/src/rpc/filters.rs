//! Server-side `getEvents` filter construction (issue #203).
//!
//! The Soroban RPC accepts a `filters` array on `getEvents`. Each filter narrows
//! the result set by event type, emitting contract, and topic pattern:
//!
//! ```json
//! { "type": "contract", "contractIds": ["C..."], "topics": [["AAAA...", "*"]] }
//! ```
//!
//! Pushing the indexer's contract allowlist into that array means the RPC never
//! sends us events we would immediately discard, which saves bandwidth, RPC
//! quota, and the CPU cost of XDR-decoding throwaway payloads.
//!
//! The RPC caps both the number of filters per request and the number of
//! contract IDs per filter, so a large allowlist is sharded across several
//! filters. An allowlist too large to express within those caps degrades to
//! sending no filter at all (index-all); the client-side allowlist check in the
//! streamer remains the correctness boundary in that case.

use std::collections::HashSet;

use base64::{engine::general_purpose::STANDARD, Engine};
use serde::Serialize;
use stellar_xdr::curr::{Limited, Limits, ScSymbol, ScVal, WriteXdr};

/// Maximum contract IDs the Soroban RPC accepts inside a single filter.
pub const MAX_CONTRACT_IDS_PER_FILTER: usize = 5;

/// Maximum filter objects the Soroban RPC accepts in one `getEvents` request.
pub const MAX_FILTERS_PER_REQUEST: usize = 5;

/// Largest allowlist that can be expressed server-side before we degrade to
/// index-all plus client-side filtering.
pub const MAX_FILTERABLE_CONTRACTS: usize = MAX_CONTRACT_IDS_PER_FILTER * MAX_FILTERS_PER_REQUEST;

/// Wildcard segment matching exactly one topic position.
const SEGMENT_ANY: &str = "*";

/// Wildcard segment matching zero or more trailing topic positions.
const SEGMENT_ANY_TRAILING: &str = "**";

/// A single Soroban `getEvents` filter object, serialised in the RPC's shape.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct EventFilter {
    #[serde(rename = "type")]
    pub event_type: &'static str,
    #[serde(rename = "contractIds", skip_serializing_if = "Vec::is_empty")]
    pub contract_ids: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub topics: Vec<Vec<String>>,
}

/// The filter set for one `getEvents` request plus whether we had to give up on
/// server-side filtering for this allowlist.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FilterPlan {
    pub filters: Vec<EventFilter>,
    /// True when an allowlist was present but could not be expressed within the
    /// RPC's filter caps, so the request falls back to index-all.
    pub degraded: bool,
}

impl FilterPlan {
    /// The index-all plan: no filters, no degradation.
    pub fn index_all() -> Self {
        Self {
            filters: Vec::new(),
            degraded: false,
        }
    }
}

/// Build the `getEvents` filter set for an allowlist and optional topic patterns.
///
/// - `allowlist` `None` or empty → index-all (no filters emitted).
/// - Contract IDs are sorted so the request body is deterministic, then sharded
///   into chunks of [`MAX_CONTRACT_IDS_PER_FILTER`].
/// - Topic patterns, when configured, are attached to every shard. Topic-only
///   (contract-agnostic) filtering is deliberately not supported — an empty
///   allowlist always means index-all.
pub fn build_event_filters(
    allowlist: Option<&HashSet<String>>,
    topics: &[Vec<String>],
) -> FilterPlan {
    let Some(set) = allowlist.filter(|s| !s.is_empty()) else {
        return FilterPlan::index_all();
    };

    if set.len() > MAX_FILTERABLE_CONTRACTS {
        return FilterPlan {
            filters: Vec::new(),
            degraded: true,
        };
    }

    let mut ids: Vec<String> = set.iter().cloned().collect();
    ids.sort();

    let filters = ids
        .chunks(MAX_CONTRACT_IDS_PER_FILTER)
        .map(|chunk| EventFilter {
            event_type: "contract",
            contract_ids: chunk.to_vec(),
            topics: topics.to_vec(),
        })
        .collect();

    FilterPlan {
        filters,
        degraded: false,
    }
}

/// Parse the configured topic-filter specification into RPC topic patterns.
///
/// The spec is a comma-separated list of patterns; each pattern is a
/// `/`-separated list of segments. A segment is either a wildcard (`*` for one
/// position, `**` for the trailing remainder) or a Soroban symbol, which is
/// XDR-encoded and base64'd to match what the RPC compares against.
///
/// `"transfer/*/*"` becomes one pattern of three segments: the encoded
/// `Symbol("transfer")` followed by two single-position wildcards.
pub fn parse_topic_filters(spec: &str) -> Result<Vec<Vec<String>>, String> {
    let mut patterns = Vec::new();

    for raw_pattern in spec.split(',') {
        let pattern = raw_pattern.trim();
        if pattern.is_empty() {
            continue;
        }

        let mut segments = Vec::new();
        for raw_segment in pattern.split('/') {
            let segment = raw_segment.trim();
            if segment.is_empty() {
                return Err(format!("empty topic segment in pattern {pattern:?}"));
            }
            if segment == SEGMENT_ANY || segment == SEGMENT_ANY_TRAILING {
                segments.push(segment.to_string());
            } else {
                segments.push(encode_symbol_topic(segment)?);
            }
        }

        if segments.len() > 4 {
            return Err(format!(
                "topic pattern {pattern:?} has {} segments; the RPC allows at most 4",
                segments.len()
            ));
        }

        patterns.push(segments);
    }

    Ok(patterns)
}

/// XDR-encode a symbol into the base64 form the RPC matches topics against.
fn encode_symbol_topic(symbol: &str) -> Result<String, String> {
    let val = ScVal::Symbol(
        ScSymbol::try_from(symbol.to_string())
            .map_err(|_| format!("invalid topic symbol {symbol:?}: not a valid Soroban symbol"))?,
    );
    let mut buf = Vec::new();
    val.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
        .map_err(|e| format!("failed to XDR-encode topic symbol {symbol:?}: {e}"))?;
    Ok(STANDARD.encode(buf))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn allowlist(ids: &[&str]) -> HashSet<String> {
        ids.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn empty_allowlist_yields_index_all() {
        let plan = build_event_filters(None, &[]);
        assert!(plan.filters.is_empty());
        assert!(!plan.degraded);
    }

    #[test]
    fn empty_set_yields_index_all() {
        let set = HashSet::new();
        let plan = build_event_filters(Some(&set), &[]);
        assert!(plan.filters.is_empty());
        assert!(!plan.degraded);
    }

    #[test]
    fn single_contract_produces_one_filter() {
        let set = allowlist(&["CAAA"]);
        let plan = build_event_filters(Some(&set), &[]);
        assert_eq!(plan.filters.len(), 1);
        assert_eq!(plan.filters[0].contract_ids, vec!["CAAA".to_string()]);
        assert_eq!(plan.filters[0].event_type, "contract");
    }

    #[test]
    fn contract_ids_are_sorted_for_deterministic_requests() {
        let set = allowlist(&["CCCC", "CAAA", "CBBB"]);
        let plan = build_event_filters(Some(&set), &[]);
        assert_eq!(plan.filters[0].contract_ids, vec!["CAAA", "CBBB", "CCCC"]);
    }

    #[test]
    fn allowlist_at_the_per_filter_limit_stays_one_filter() {
        let set = allowlist(&["C1", "C2", "C3", "C4", "C5"]);
        let plan = build_event_filters(Some(&set), &[]);
        assert_eq!(plan.filters.len(), 1);
        assert_eq!(plan.filters[0].contract_ids.len(), 5);
    }

    #[test]
    fn allowlist_over_the_per_filter_limit_is_sharded() {
        let set = allowlist(&["C1", "C2", "C3", "C4", "C5", "C6"]);
        let plan = build_event_filters(Some(&set), &[]);
        assert_eq!(plan.filters.len(), 2, "6 contracts shard into 2 filters");
        assert_eq!(plan.filters[0].contract_ids.len(), 5);
        assert_eq!(plan.filters[1].contract_ids, vec!["C6".to_string()]);
        assert!(!plan.degraded);
    }

    #[test]
    fn allowlist_at_the_request_limit_fills_every_filter() {
        let ids: Vec<String> = (0..MAX_FILTERABLE_CONTRACTS)
            .map(|i| format!("C{i:03}"))
            .collect();
        let set: HashSet<String> = ids.into_iter().collect();
        let plan = build_event_filters(Some(&set), &[]);
        assert_eq!(plan.filters.len(), MAX_FILTERS_PER_REQUEST);
        assert!(plan
            .filters
            .iter()
            .all(|f| f.contract_ids.len() == MAX_CONTRACT_IDS_PER_FILTER));
        assert!(!plan.degraded);
    }

    #[test]
    fn allowlist_beyond_the_request_limit_degrades_to_index_all() {
        let ids: Vec<String> = (0..MAX_FILTERABLE_CONTRACTS + 1)
            .map(|i| format!("C{i:03}"))
            .collect();
        let set: HashSet<String> = ids.into_iter().collect();
        let plan = build_event_filters(Some(&set), &[]);
        assert!(plan.filters.is_empty());
        assert!(plan.degraded, "oversized allowlist must flag degradation");
    }

    #[test]
    fn topic_patterns_are_attached_to_every_shard() {
        let set = allowlist(&["C1", "C2", "C3", "C4", "C5", "C6"]);
        let topics = parse_topic_filters("transfer/*/*").unwrap();
        let plan = build_event_filters(Some(&set), &topics);
        assert_eq!(plan.filters.len(), 2);
        assert!(plan.filters.iter().all(|f| f.topics == topics));
    }

    #[test]
    fn serialised_filter_matches_the_rpc_shape() {
        let set = allowlist(&["CAAA"]);
        let plan = build_event_filters(Some(&set), &[]);
        let json = serde_json::to_value(&plan.filters[0]).unwrap();
        assert_eq!(json["type"], "contract");
        assert_eq!(json["contractIds"][0], "CAAA");
        assert!(
            json.get("topics").is_none(),
            "an empty topic list must be omitted, not sent as []"
        );
    }

    #[test]
    fn empty_topic_spec_yields_no_patterns() {
        assert!(parse_topic_filters("").unwrap().is_empty());
        assert!(parse_topic_filters("  ,  ").unwrap().is_empty());
    }

    #[test]
    fn symbol_segments_are_xdr_base64_encoded() {
        // A symbol encodes as: discriminant (SCV_SYMBOL = 15), byte length, then
        // the body zero-padded to a 4-byte boundary. "transfer" is 8 bytes, so
        // it lands exactly on the boundary and carries no padding.
        let patterns = parse_topic_filters("transfer").unwrap();
        assert_eq!(patterns.len(), 1);
        let encoded = &patterns[0][0];
        assert_ne!(encoded, "transfer", "segment must be XDR-encoded");

        let bytes = STANDARD.decode(encoded).expect("valid base64");
        assert_eq!(&bytes[0..4], &[0, 0, 0, 15], "SCV_SYMBOL discriminant");
        assert_eq!(&bytes[4..8], &[0, 0, 0, 8], "symbol length");
        assert_eq!(&bytes[8..], b"transfer", "symbol body");
    }

    #[test]
    fn symbol_segments_are_padded_to_a_four_byte_boundary() {
        // "mint" is 4 bytes (no padding); "approve" is 7 and must be padded to 8.
        let patterns = parse_topic_filters("mint,approve").unwrap();

        let mint = STANDARD.decode(&patterns[0][0]).expect("valid base64");
        assert_eq!(&mint[4..8], &[0, 0, 0, 4], "symbol length");
        assert_eq!(&mint[8..], b"mint", "4-byte body needs no padding");

        let approve = STANDARD.decode(&patterns[1][0]).expect("valid base64");
        assert_eq!(&approve[4..8], &[0, 0, 0, 7], "length is the unpadded size");
        assert_eq!(&approve[8..], b"approve\0", "7-byte body padded to 8");
    }

    #[test]
    fn wildcard_segments_pass_through_unencoded() {
        let patterns = parse_topic_filters("*/**").unwrap();
        assert_eq!(patterns[0], vec!["*".to_string(), "**".to_string()]);
    }

    #[test]
    fn multiple_patterns_split_on_comma() {
        let patterns = parse_topic_filters("transfer/*, mint/*").unwrap();
        assert_eq!(patterns.len(), 2);
        assert_eq!(patterns[0].len(), 2);
        assert_eq!(patterns[1].len(), 2);
    }

    #[test]
    fn empty_segment_is_rejected() {
        assert!(parse_topic_filters("transfer//to").is_err());
    }

    #[test]
    fn pattern_longer_than_four_segments_is_rejected() {
        assert!(parse_topic_filters("a/b/c/d/e").is_err());
    }
}
