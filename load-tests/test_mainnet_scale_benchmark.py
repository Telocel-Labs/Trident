#!/usr/bin/env python3
"""
Unit and regression tests for mainnet_scale_benchmark.py (issue #672).
"""

import json
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from mainnet_scale_benchmark import (
    parse_scale,
    extract_plan_node_types,
    run_benchmark,
    render_markdown_summary,
    BENCHMARK_QUERIES,
)


class TestMainnetScaleBenchmark(unittest.TestCase):
    def test_parse_scale(self):
        self.assertEqual(parse_scale('1m'), 1_000_000)
        self.assertEqual(parse_scale('5m'), 5_000_000)
        self.assertEqual(parse_scale('10m'), 10_000_000)
        self.assertEqual(parse_scale('20m'), 20_000_000)
        self.assertEqual(parse_scale('500000'), 500_000)
        with self.assertRaises(ValueError):
            parse_scale('invalid')

    def test_extract_plan_node_types(self):
        sample_plan = {
            "Node Type": "Limit",
            "Plans": [
                {
                    "Node Type": "Append",
                    "Plans": [
                        {
                            "Node Type": "Index Scan",
                            "Index Name": "idx_soroban_events_contract_ledger",
                        },
                        {
                            "Node Type": "Bitmap Heap Scan",
                            "Plans": [
                                {
                                    "Node Type": "Bitmap Index Scan",
                                }
                            ]
                        }
                    ]
                }
            ]
        }
        nodes = extract_plan_node_types(sample_plan)
        self.assertIn("Limit", nodes)
        self.assertIn("Append", nodes)
        self.assertIn("Index Scan", nodes)
        self.assertIn("Bitmap Heap Scan", nodes)
        self.assertIn("Bitmap Index Scan", nodes)
        self.assertNotIn("Seq Scan", nodes)

    def test_dry_run_benchmark(self):
        report = run_benchmark(
            db_url="postgres://dummy",
            target_rows=10_000_000,
            iterations=5,
            dry_run=True,
        )
        self.assertTrue(report["summary"]["all_passed"])
        self.assertEqual(report["summary"]["passed"], len(BENCHMARK_QUERIES))
        self.assertEqual(report["summary"]["failed"], 0)
        self.assertEqual(report["target_scale_rows"], 10_000_000)

        # Markdown rendering check
        md = render_markdown_summary(report)
        self.assertIn("Mainnet-Scale Database Benchmark", md)
        self.assertIn("10,000,000 events", md)
        self.assertIn("Index Scan", md)

    def test_cli_execution(self):
        script_path = os.path.join(os.path.dirname(__file__), "mainnet_scale_benchmark.py")
        res = subprocess.run(
            [sys.executable, script_path, "--dry-run", "--scale", "10m"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(res.returncode, 0, f"CLI dry run failed: {res.stderr}")
        self.assertIn("10,000,000 rows", res.stdout)
        self.assertIn("Benchmark PASSED", res.stdout)


if __name__ == "__main__":
    unittest.main()
