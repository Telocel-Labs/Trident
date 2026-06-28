"""Domain types for the Trident Python SDK."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal, Optional

Network = Literal["mainnet", "testnet", "futurenet"]


@dataclass(frozen=True)
class SorobanEvent:
    id: str
    contract_id: str
    ledger_sequence: int
    ledger_timestamp: str
    transaction_hash: str
    event_index: int
    event_type: str
    topics: list[str]
    data: Any
    created_at: str

    @classmethod
    def from_api(cls, raw: dict[str, Any]) -> "SorobanEvent":
        import json as _json

        data = raw.get("data", "")
        if isinstance(data, str):
            try:
                data = _json.loads(data)
            except (_json.JSONDecodeError, ValueError):
                pass

        return cls(
            id=raw["id"],
            contract_id=raw["contract_id"],
            ledger_sequence=int(raw["ledger_sequence"]),
            ledger_timestamp=raw["ledger_timestamp"],
            transaction_hash=raw["transaction_hash"],
            event_index=int(raw["event_index"]),
            event_type=raw["event_type"],
            topics=list(raw.get("topics", [])),
            data=data,
            created_at=raw["created_at"],
        )


@dataclass
class PaginatedEvents:
    events: list[SorobanEvent] = field(default_factory=list)
    cursor: Optional[str] = None
    has_more: bool = False
