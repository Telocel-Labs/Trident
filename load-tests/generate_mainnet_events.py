#!/usr/bin/env python3
"""
Generate representative mainnet-scale event data across diverse contract categories (issue #673).
Produces SAC tokens, NFTs, AMM swaps, DAO governance, and system/diagnostic events matching real mainnet ratios.
"""
import argparse
import json
import os
import random
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
FIXTURE_PATH = SCRIPT_DIR / "fixtures" / "mainnet_event_corpus.json"


def load_templates():
    if not FIXTURE_PATH.is_file():
        raise FileNotFoundError(f"Fixture not found at {FIXTURE_PATH}")
    with open(FIXTURE_PATH, "r", encoding="utf-8") as f:
        data = json.load(f)
    return data["event_templates"], data["event_mix_ratio"]


def generate_event(template, ledger_seq, event_idx, network="mainnet"):
    tx_hash = f"{random.getrandbits(256):064x}"
    ts = datetime.fromtimestamp(1700000000 + ledger_seq, tz=timezone.utc).isoformat()
    
    # Clone template values with minor jitter
    topics = list(template["topics"])
    data = template["data"]
    if isinstance(data, dict):
        data = dict(data)
        if "amount" in data and str(data["amount"]).isdigit():
            data["amount"] = str(int(data["amount"]) + random.randint(1, 1000000))
        if "token_id" in data:
            data["token_id"] = event_idx + 1000
    
    return {
        "contract_id": template["contract_id"],
        "ledger_sequence": ledger_seq,
        "ledger_timestamp": ts,
        "transaction_hash": tx_hash,
        "event_index": event_idx,
        "event_type": template["event_type"],
        "network": network,
        "topics": topics,
        "data": data,
        "name": template["name"],
        "category": template["category"],
    }


def main():
    parser = argparse.ArgumentParser(description="Generate diverse mainnet-like event corpus for load testing (issue #673)")
    parser.add_argument("--count", type=int, default=1000, help="Number of events to generate")
    parser.add_argument("--network", default="mainnet", choices=["mainnet", "testnet", "futurenet", "sandbox"])
    parser.add_argument("--start-ledger", type=int, default=52000000, help="Initial ledger sequence")
    parser.add_argument("--format", choices=["json", "sql"], default="json", help="Output format")
    parser.add_argument("--out", type=str, default="", help="Output destination file (or stdout if omitted)")
    parser.add_argument("--database-url", type=str, default=os.getenv("DATABASE_URL", ""), help="Direct insert into database URL if provided")
    args = parser.parse_args()

    templates, ratios = load_templates()
    
    # Build distribution weights
    weights = []
    for tmpl in templates:
        cat = tmpl["category"]
        weight = ratios.get(cat, 0.1) / max(1, sum(1 for t in templates if t["category"] == cat))
        weights.append(weight)

    events = []
    cur_ledger = args.start_ledger
    events_in_cur_ledger = 0
    max_events_per_ledger = random.randint(5, 20)

    for i in range(args.count):
        tmpl = random.choices(templates, weights=weights, k=1)[0]
        ev = generate_event(tmpl, cur_ledger, events_in_cur_ledger, args.network)
        events.append(ev)
        events_in_cur_ledger += 1
        if events_in_cur_ledger >= max_events_per_ledger:
            cur_ledger += 1
            events_in_cur_ledger = 0
            max_events_per_ledger = random.randint(5, 20)

    if args.database_url:
        print(f"Connecting to database to insert {len(events)} events...", file=sys.stderr)
        try:
            import psycopg2
            from psycopg2.extras import execute_values
            conn = psycopg2.connect(args.database_url)
            cur = conn.cursor()
            insert_sql = """
                INSERT INTO soroban_events
                    (contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
                     event_index, event_type, network, topics, data)
                VALUES %s
                ON CONFLICT (ledger_sequence, transaction_hash, event_index, network) DO NOTHING
            """
            values = [
                (
                    e["contract_id"], e["ledger_sequence"], e["ledger_timestamp"],
                    e["transaction_hash"], e["event_index"], e["event_type"],
                    e["network"], json.dumps(e["topics"]), json.dumps(e["data"])
                )
                for e in events
            ]
            execute_values(cur, insert_sql, values, page_size=500)
            conn.commit()
            cur.close()
            conn.close()
            print(f"Successfully inserted {len(events)} diverse events into database.", file=sys.stderr)
        except ImportError:
            print("psycopg2 not installed; falling back to file/stdout export", file=sys.stderr)

    if args.format == "json":
        output = json.dumps(events, indent=2)
    else: # sql
        sql_lines = ["-- Generated varied mainnet events (issue #673)"]
        for e in events:
            top_json = json.dumps(e["topics"]).replace("'", "''")
            data_json = json.dumps(e["data"]).replace("'", "''")
            sql_lines.append(
                f"INSERT INTO soroban_events (contract_id, ledger_sequence, ledger_timestamp, transaction_hash, event_index, event_type, network, topics, data) "
                f"VALUES ('{e['contract_id']}', {e['ledger_sequence']}, '{e['ledger_timestamp']}', '{e['transaction_hash']}', {e['event_index']}, '{e['event_type']}', '{e['network']}', '{top_json}', '{data_json}') "
                f"ON CONFLICT DO NOTHING;"
            )
        output = "\n".join(sql_lines)

    if args.out:
        Path(args.out).write_text(output, encoding="utf-8")
        print(f"Wrote {len(events)} events to {args.out}", file=sys.stderr)
    elif not args.database_url:
        print(output)


if __name__ == "__main__":
    main()
