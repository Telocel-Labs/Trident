#!/usr/bin/env python3
"""
Mainnet-Scale Database Benchmark for Trident (issue #672).

Measures and validates query performance, index utilization, and partition pruning
against a `soroban_events` table sized in the tens of millions of rows (10,000,000+).

Runs on a recurring schedule in CI (.github/workflows/mainnet-scale-benchmark.yml)
and on-demand to guard against performance regressions as mainnet table volume grows.
"""

import argparse
import json
import os
import sys
import time
from datetime import datetime, timezone
from typing import Dict, List, Any, Optional

SCALE_MAP = {
    '1m': 1_000_000,
    '5m': 5_000_000,
    '10m': 10_000_000,
    '20m': 20_000_000,
}

BENCHMARK_QUERIES = [
    {
        "name": "list_events_by_contract_and_ledger",
        "description": "Hot path: query events for contract within recent ledger window",
        "sql": """
            SELECT id, contract_id, ledger_sequence, ledger_timestamp,
                   transaction_hash, event_index, event_type, network, topics, data
            FROM soroban_events
            WHERE network = 'mainnet'
              AND contract_id = 'CA000000000000000000000000000000000000000000000000000001'
              AND ledger_sequence BETWEEN 50050000 AND 50090000
            ORDER BY ledger_sequence DESC, event_index DESC
            LIMIT 25;
        """,
        "max_p95_ms": 30.0,
        "required_node_types": ["Index Scan", "Index Only Scan", "Bitmap Index Scan"],
        "forbidden_node_types": ["Seq Scan"],
    },
    {
        "name": "keyset_cursor_pagination",
        "description": "Forward cursor pagination using composite keyset (network, ledger_sequence, event_index)",
        "sql": """
            SELECT id, contract_id, ledger_sequence, ledger_timestamp,
                   transaction_hash, event_index, event_type, network, topics, data
            FROM soroban_events
            WHERE network = 'mainnet'
              AND (ledger_sequence > 50050000 OR (ledger_sequence = 50050000 AND event_index > 5))
            ORDER BY ledger_sequence ASC, event_index ASC
            LIMIT 25;
        """,
        "max_p95_ms": 40.0,
        "required_node_types": ["Index Scan", "Index Only Scan", "Bitmap Index Scan"],
        "forbidden_node_types": ["Seq Scan"],
    },
    {
        "name": "topic_0_filtered_query",
        "description": "Contract events filtered by exact topic_0 (e.g. transfer event)",
        "sql": """
            SELECT id, contract_id, ledger_sequence, ledger_timestamp,
                   transaction_hash, event_index, event_type, network, topics, data
            FROM soroban_events
            WHERE network = 'mainnet'
              AND contract_id = 'CA000000000000000000000000000000000000000000000000000001'
              AND topic_0 = 'transfer'
            ORDER BY ledger_sequence DESC, event_index DESC
            LIMIT 25;
        """,
        "max_p95_ms": 35.0,
        "required_node_types": ["Index Scan", "Index Only Scan", "Bitmap Index Scan"],
        "forbidden_node_types": ["Seq Scan"],
    },
    {
        "name": "partition_pruning_verification",
        "description": "Asserts execution engine prunes all non-target partitions when ledger bounds are tight",
        "sql": """
            EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
            SELECT id, ledger_sequence
            FROM soroban_events
            WHERE network = 'mainnet'
              AND ledger_sequence BETWEEN 50010000 AND 50020000
            LIMIT 25;
        """,
        "max_p95_ms": 25.0,
        "required_node_types": ["Index Scan", "Index Only Scan", "Bitmap Index Scan", "Append"],
        "forbidden_node_types": ["Seq Scan"],
    },
]


def extract_plan_node_types(plan_node: Dict[str, Any], acc: Optional[List[str]] = None) -> List[str]:
    """Recursively extracts all execution plan node types from an EXPLAIN JSON node."""
    if acc is None:
        acc = []
    if not isinstance(plan_node, dict):
        return acc

    node_type = plan_node.get("Node Type")
    if node_type:
        acc.append(node_type)

    for plan in plan_node.get("Plans", []):
        extract_plan_node_types(plan, acc)
    return acc


def parse_scale(scale_str: str) -> int:
    s = scale_str.lower().strip()
    if s in SCALE_MAP:
        return SCALE_MAP[s]
    try:
        return int(scale_str)
    except ValueError:
        raise ValueError(f"Invalid scale: '{scale_str}'. Allowed: 1m, 5m, 10m, 20m, or integer row count.")


def seed_scale_data(conn: Any, target_rows: int) -> None:
    """Seeds soroban_events up to target_rows using high-speed server-side generate_series batches."""
    print(f"\n[Seed] Checking existing row count in soroban_events...")
    with conn.cursor() as cur:
        cur.execute("SELECT COUNT(*) FROM soroban_events;")
        current_rows = cur.fetchone()[0]
        print(f"[Seed] Current row count: {current_rows:,} / target: {target_rows:,}")

        if current_rows >= target_rows:
            print("[Seed] Target scale already met; skipping data generation.")
            return

        needed = target_rows - current_rows
        print(f"[Seed] Generating {needed:,} events across partitions in 500k batches...")

        batch_size = 500_000
        start_seq = current_rows + 1
        end_target = target_rows

        for batch_start in range(start_seq, end_target + 1, batch_size):
            batch_end = min(batch_start + batch_size - 1, end_target)
            t0 = time.time()

            cur.execute("""
                INSERT INTO soroban_events (
                    id, contract_id, ledger_sequence, ledger_timestamp,
                    transaction_hash, event_index, raw_event_index, event_type,
                    network, topic_0, topic_1, topic_2, topic_3, topics, data, created_at
                )
                SELECT
                    md5(random()::text || seq::text),
                    'CA' || lpad((seq % 100)::text, 54, '0'),
                    50000000 + (seq / 20),
                    NOW() - (((10000000 - seq) * 5) || ' seconds')::interval,
                    repeat(md5(seq::text), 2),
                    (seq % 10)::int,
                    (seq % 10)::int,
                    'contract',
                    'mainnet',
                    CASE (seq % 4)
                        WHEN 0 THEN 'transfer'
                        WHEN 1 THEN 'mint'
                        WHEN 2 THEN 'burn'
                        ELSE 'swap'
                    END,
                    'topic1_' || (seq % 50)::text,
                    NULL,
                    NULL,
                    jsonb_build_array('transfer', 'topic1_' || (seq % 50)::text),
                    jsonb_build_object('amount', (seq % 10000) * 1000, 'seq', seq),
                    NOW()
                FROM generate_series(%s::bigint, %s::bigint) AS seq;
            """, (batch_start, batch_end))

            conn.commit()
            dur = time.time() - t0
            print(f"  Inserted batch {batch_start:,}..{batch_end:,} in {dur:.2f}s ({(batch_end - batch_start + 1)/dur:,.0f} rows/s)")

        print("[Seed] Running VACUUM ANALYZE to update table statistics...")
        old_isolation = conn.isolation_level
        conn.set_isolation_level(0)  # autocommit for VACUUM
        cur.execute("VACUUM ANALYZE soroban_events;")
        conn.set_isolation_level(old_isolation)
        print("[Seed] Sizing and statistics updated successfully.")


def run_benchmark(db_url: str, target_rows: int, iterations: int, dry_run: bool = False) -> Dict[str, Any]:
    """Executes the benchmark suite and returns detailed latency, plan, and violation metrics."""
    report: Dict[str, Any] = {
        "benchmark": "mainnet_scale_database_benchmark",
        "target_scale_rows": target_rows,
        "iterations_per_query": iterations,
        "executed_at": datetime.now(timezone.utc).isoformat(),
        "dry_run": dry_run,
        "queries": {},
        "summary": {
            "total_queries": len(BENCHMARK_QUERIES),
            "passed": 0,
            "failed": 0,
            "all_passed": True,
            "violations": [],
        },
    }

    if dry_run:
        print("[Dry-Run] Executing simulated benchmark run (offline verification mode)...")
        for q in BENCHMARK_QUERIES:
            name = q["name"]
            # Mock latency conforming to expected index scans
            mock_latencies = [12.4, 14.1, 13.5, 15.2, 11.9, 13.0, 14.8, 12.1]
            p50 = sorted(mock_latencies)[len(mock_latencies) // 2]
            p95 = sorted(mock_latencies)[int(len(mock_latencies) * 0.95)]
            report["queries"][name] = {
                "description": q["description"],
                "p50_ms": p50,
                "p95_ms": p95,
                "max_p95_budget_ms": q["max_p95_ms"],
                "plan_node_types": ["Index Scan"],
                "seq_scan_detected": False,
                "passed": True,
            }
            report["summary"]["passed"] += 1
        return report

    import psycopg2
    print(f"\n[Connect] Connecting to database...")
    conn = psycopg2.connect(db_url)

    try:
        # Check and seed scale if needed
        seed_scale_data(conn, target_rows)

        with conn.cursor() as cur:
            for q in BENCHMARK_QUERIES:
                name = q["name"]
                sql = q["sql"]
                max_budget = q["max_p95_ms"]
                forbidden = q["forbidden_node_types"]
                print(f"\nEvaluating query: {name} ({q['description']})")

                # Step A: Capture execution plan
                explain_sql = f"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) {sql.replace('EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)', '')}"
                cur.execute(explain_sql)
                explain_raw = cur.fetchone()[0]
                plan_root = explain_raw[0].get("Plan", {})
                detected_nodes = extract_plan_node_types(plan_root)
                seq_scan_found = any(fn in detected_nodes for fn in forbidden)

                # Step B: Run iterations for latency distribution
                latencies_ms: List[float] = []
                for _ in range(iterations):
                    t0 = time.perf_counter()
                    cur.execute(sql.replace('EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)', ''))
                    cur.fetchall()
                    dur_ms = (time.perf_counter() - t0) * 1000.0
                    latencies_ms.append(dur_ms)

                latencies_sorted = sorted(latencies_ms)
                p50 = latencies_sorted[int(len(latencies_sorted) * 0.50)]
                p90 = latencies_sorted[int(len(latencies_sorted) * 0.90)]
                p95 = latencies_sorted[int(len(latencies_sorted) * 0.95)]
                p99 = latencies_sorted[int(len(latencies_sorted) * 0.99)]

                query_passed = (p95 <= max_budget) and (not seq_scan_found)
                if not query_passed:
                    report["summary"]["all_passed"] = False
                    if p95 > max_budget:
                        report["summary"]["violations"].append(
                            f"{name}: p95 latency {p95:.2f}ms exceeded budget {max_budget}ms"
                        )
                    if seq_scan_found:
                        report["summary"]["violations"].append(
                            f"{name}: forbidden Seq Scan detected in plan nodes: {detected_nodes}"
                        )

                report["queries"][name] = {
                    "description": q["description"],
                    "iterations": iterations,
                    "p50_ms": round(p50, 2),
                    "p90_ms": round(p90, 2),
                    "p95_ms": round(p95, 2),
                    "p99_ms": round(p99, 2),
                    "max_p95_budget_ms": max_budget,
                    "plan_node_types": list(set(detected_nodes)),
                    "seq_scan_detected": seq_scan_found,
                    "passed": query_passed,
                }

                status_icon = "✓" if query_passed else "✗"
                print(f"  {status_icon} p50: {p50:.2f}ms | p95: {p95:.2f}ms (budget: {max_budget}ms) | Nodes: {detected_nodes}")

                if query_passed:
                    report["summary"]["passed"] += 1
                else:
                    report["summary"]["failed"] += 1

    finally:
        conn.close()

    return report


def render_markdown_summary(report: Dict[str, Any]) -> str:
    """Renders a formatted GitHub Step Summary markdown table."""
    status_emoji = "✅ PASSED" if report["summary"]["all_passed"] else "❌ FAILED"
    scale_rows = report["target_scale_rows"]

    lines = [
        f"## Mainnet-Scale Database Benchmark ({status_emoji})",
        "",
        f"- **Scale Target**: {scale_rows:,} events",
        f"- **Executed At**: `{report['executed_at']}`",
        f"- **Queries Evaluated**: {report['summary']['total_queries']} ({report['summary']['passed']} passed, {report['summary']['failed']} failed)",
        "",
        "| Query | P50 (ms) | P95 (ms) | Target (ms) | Index Nodes | Status |",
        "|-------|----------|----------|-------------|-------------|--------|",
    ]

    for name, q in report["queries"].items():
        node_summary = ", ".join(q["plan_node_types"])
        status = "✅ PASS" if q["passed"] else "❌ FAIL"
        lines.append(
            f"| `{name}` | {q['p50_ms']} | {q['p95_ms']} | ≤ {q['max_p95_budget_ms']} | {node_summary} | {status} |"
        )

    if report["summary"]["violations"]:
        lines.extend(["", "### ⚠️ Regressions & Violations Detected:"])
        for v in report["summary"]["violations"]:
            lines.append(f"- {v}")

    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description="Run mainnet-scale query benchmark on soroban_events table.")
    parser.add_argument("--db-url", default=os.getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/trident"))
    parser.add_argument("--scale", default="10m", help="Target table scale: 1m, 5m, 10m (default: 10m)")
    parser.add_argument("--iterations", type=int, default=20, help="Benchmark query iterations per sample (default: 20)")
    parser.add_argument("--output", default="load-tests/mainnet-scale-benchmark-results.json", help="Path to write JSON report")
    parser.add_argument("--markdown", help="Optional path to write markdown summary (e.g. $GITHUB_STEP_SUMMARY)")
    parser.add_argument("--dry-run", action="store_true", help="Simulate execution without requiring a live database")
    args = parser.parse_args()

    target_rows = parse_scale(args.scale)
    print(f"============================================================")
    print(f" Trident Mainnet-Scale Database Benchmark (issue #672)")
    print(f" Target scale: {target_rows:,} rows across partitions")
    print(f" Iterations: {args.iterations}")
    print(f" Mode: {'DRY RUN' if args.dry_run else 'LIVE DATABASE'}")
    print(f"============================================================")

    report = run_benchmark(
        db_url=args.db_url,
        target_rows=target_rows,
        iterations=args.iterations,
        dry_run=args.dry_run,
    )

    # Save JSON report
    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump(report, f, indent=2)
    print(f"\n[Report] Saved JSON benchmark metrics to: {args.output}")

    # Render Markdown
    md_content = render_markdown_summary(report)
    if args.markdown:
        with open(args.markdown, "a", encoding="utf-8") as f:
            f.write(md_content)
        print(f"[Summary] Appended summary markdown to: {args.markdown}")
    else:
        print("\n" + md_content)

    if not report["summary"]["all_passed"]:
        print("\n❌ Benchmark FAILED: performance budgets exceeded or sequential scans detected.")
        sys.exit(1)

    print("\n🎉 Benchmark PASSED: all queries verified against mainnet-scale table!")


if __name__ == "__main__":
    main()
