"""Shared fixtures for Trident Python SDK tests."""

import pytest

API_URL = "https://api.trident.example"
API_KEY = "test-api-key"

RAW_EVENT = {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "contract_id": "CABC1234567890ABCDEF",
    "ledger_sequence": 100,
    "ledger_timestamp": "2026-01-01T00:00:00Z",
    "transaction_hash": "deadbeef",
    "event_index": 0,
    "event_type": "contract",
    "topics": ["transfer"],
    "data": '{"amount": 100}',
    "created_at": "2026-01-01T00:00:01Z",
}

LIST_RESPONSE = {
    "events": [RAW_EVENT],
    "next_cursor": "cursor123",
    "has_more": True,
}
