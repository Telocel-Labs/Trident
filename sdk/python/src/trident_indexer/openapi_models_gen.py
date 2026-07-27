from dataclasses import dataclass
from typing import Any, TypeVar, Callable, Type, cast
from enum import Enum
from uuid import UUID


T = TypeVar("T")
EnumT = TypeVar("EnumT", bound=Enum)


def from_str(x: Any) -> str:
    assert isinstance(x, str)
    return x


def from_int(x: Any) -> int:
    assert isinstance(x, int) and not isinstance(x, bool)
    return x


def from_list(f: Callable[[Any], T], x: Any) -> list[T]:
    assert isinstance(x, list)
    return [f(y) for y in x]


def to_class(c: Type[T], x: Any) -> dict:
    assert isinstance(x, c)
    return cast(Any, x).to_dict()


def to_enum(c: Type[EnumT], x: Any) -> EnumT:
    assert isinstance(x, c)
    return x.value


def from_none(x: Any) -> Any:
    assert x is None
    return x


def from_union(fs, x):
    for f in fs:
        try:
            return f(x)
        except:
            pass
    assert False


def from_bool(x: Any) -> bool:
    assert isinstance(x, bool)
    return x


@dataclass
class ContractStats:
    contract_id: str
    """Soroban contract address"""

    event_count: int
    """Total events for this contract in range"""

    last_seen_at: str
    """Timestamp of last event for this contract"""

    last_seen_ledger: int
    """Latest ledger sequence for this contract"""

    @staticmethod
    def from_dict(obj: Any) -> 'ContractStats':
        assert isinstance(obj, dict)
        contract_id = from_str(obj.get("contract_id"))
        event_count = from_int(obj.get("event_count"))
        last_seen_at = from_str(obj.get("last_seen_at"))
        last_seen_ledger = from_int(obj.get("last_seen_ledger"))
        return ContractStats(contract_id, event_count, last_seen_at, last_seen_ledger)

    def to_dict(self) -> dict:
        result: dict = {}
        result["contract_id"] = from_str(self.contract_id)
        result["event_count"] = from_int(self.event_count)
        result["last_seen_at"] = from_str(self.last_seen_at)
        result["last_seen_ledger"] = from_int(self.last_seen_ledger)
        return result


class Network(Enum):
    """Network queried"""

    MAINNET = "mainnet"
    TESTNET = "testnet"


@dataclass
class ContractStatsResponse:
    contracts: list[ContractStats]
    """Contracts sorted by event count (descending)"""

    from_ledger: int
    """Lower bound of queried ledger range"""

    generated_at: str
    """Timestamp when response was generated"""

    network: Network
    """Network queried"""

    to_ledger: int
    """Upper bound of queried ledger range"""

    @staticmethod
    def from_dict(obj: Any) -> 'ContractStatsResponse':
        assert isinstance(obj, dict)
        contracts = from_list(ContractStats.from_dict, obj.get("contracts"))
        from_ledger = from_int(obj.get("from_ledger"))
        generated_at = from_str(obj.get("generated_at"))
        network = Network(obj.get("network"))
        to_ledger = from_int(obj.get("to_ledger"))
        return ContractStatsResponse(contracts, from_ledger, generated_at, network, to_ledger)

    def to_dict(self) -> dict:
        result: dict = {}
        result["contracts"] = from_list(lambda x: to_class(ContractStats, x), self.contracts)
        result["from_ledger"] = from_int(self.from_ledger)
        result["generated_at"] = from_str(self.generated_at)
        result["network"] = to_enum(Network, self.network)
        result["to_ledger"] = from_int(self.to_ledger)
        return result


@dataclass
class Error:
    code: str
    """Error code (e.g., INVALID_ARGUMENT, INTERNAL, UNAVAILABLE)"""

    message: str
    """Human-readable error message"""

    request_id: str | None = None
    """Request ID for debugging"""

    @staticmethod
    def from_dict(obj: Any) -> 'Error':
        assert isinstance(obj, dict)
        code = from_str(obj.get("code"))
        message = from_str(obj.get("message"))
        request_id = from_union([from_str, from_none], obj.get("request_id"))
        return Error(code, message, request_id)

    def to_dict(self) -> dict:
        result: dict = {}
        result["code"] = from_str(self.code)
        result["message"] = from_str(self.message)
        if self.request_id is not None:
            result["request_id"] = from_union([from_str, from_none], self.request_id)
        return result


@dataclass
class ErrorResponse:
    error: Error

    @staticmethod
    def from_dict(obj: Any) -> 'ErrorResponse':
        assert isinstance(obj, dict)
        error = Error.from_dict(obj.get("error"))
        return ErrorResponse(error)

    def to_dict(self) -> dict:
        result: dict = {}
        result["error"] = to_class(Error, self.error)
        return result


class EventType(Enum):
    """Type of event"""

    CONTRACT = "contract"
    DIAGNOSTIC = "diagnostic"
    SYSTEM = "system"


@dataclass
class SorobanEvent:
    contract_id: str
    """Soroban contract address"""

    created_at: str
    """Timestamp when event was indexed"""

    data: str
    """Event data (XDR-encoded)"""

    event_index: int
    """Event index within transaction"""

    event_type: EventType
    """Type of event"""

    id: UUID
    """Unique event identifier"""

    ledger_sequence: int
    """Ledger sequence number"""

    ledger_timestamp: str
    """Ledger timestamp in ISO 8601 format"""

    topics: list[str]
    """Event topics (XDR-encoded)"""

    transaction_hash: str
    """Transaction hash (XDR-encoded)"""

    @staticmethod
    def from_dict(obj: Any) -> 'SorobanEvent':
        assert isinstance(obj, dict)
        contract_id = from_str(obj.get("contract_id"))
        created_at = from_str(obj.get("created_at"))
        data = from_str(obj.get("data"))
        event_index = from_int(obj.get("event_index"))
        event_type = EventType(obj.get("event_type"))
        id = UUID(obj.get("id"))
        ledger_sequence = from_int(obj.get("ledger_sequence"))
        ledger_timestamp = from_str(obj.get("ledger_timestamp"))
        topics = from_list(from_str, obj.get("topics"))
        transaction_hash = from_str(obj.get("transaction_hash"))
        return SorobanEvent(contract_id, created_at, data, event_index, event_type, id, ledger_sequence, ledger_timestamp, topics, transaction_hash)

    def to_dict(self) -> dict:
        result: dict = {}
        result["contract_id"] = from_str(self.contract_id)
        result["created_at"] = from_str(self.created_at)
        result["data"] = from_str(self.data)
        result["event_index"] = from_int(self.event_index)
        result["event_type"] = to_enum(EventType, self.event_type)
        result["id"] = str(self.id)
        result["ledger_sequence"] = from_int(self.ledger_sequence)
        result["ledger_timestamp"] = from_str(self.ledger_timestamp)
        result["topics"] = from_list(from_str, self.topics)
        result["transaction_hash"] = from_str(self.transaction_hash)
        return result


@dataclass
class EventListResponse:
    events: list[SorobanEvent]
    """List of events"""

    has_more: bool
    """Whether more results are available"""

    next_cursor: str | None = None
    """Opaque cursor for next page (null if has_more is false)"""

    @staticmethod
    def from_dict(obj: Any) -> 'EventListResponse':
        assert isinstance(obj, dict)
        events = from_list(SorobanEvent.from_dict, obj.get("events"))
        has_more = from_bool(obj.get("has_more"))
        next_cursor = from_union([from_str, from_none], obj.get("next_cursor"))
        return EventListResponse(events, has_more, next_cursor)

    def to_dict(self) -> dict:
        result: dict = {}
        result["events"] = from_list(lambda x: to_class(SorobanEvent, x), self.events)
        result["has_more"] = from_bool(self.has_more)
        if self.next_cursor is not None:
            result["next_cursor"] = from_union([from_str, from_none], self.next_cursor)
        return result


@dataclass
class Indexer:
    last_ledger_indexed: int
    """Latest indexed ledger sequence"""

    last_poll_at: str | None = None
    """Timestamp of last successful indexer poll"""

    @staticmethod
    def from_dict(obj: Any) -> 'Indexer':
        assert isinstance(obj, dict)
        last_ledger_indexed = from_int(obj.get("last_ledger_indexed"))
        last_poll_at = from_union([from_str, from_none], obj.get("last_poll_at"))
        return Indexer(last_ledger_indexed, last_poll_at)

    def to_dict(self) -> dict:
        result: dict = {}
        result["last_ledger_indexed"] = from_int(self.last_ledger_indexed)
        if self.last_poll_at is not None:
            result["last_poll_at"] = from_union([from_str, from_none], self.last_poll_at)
        return result


class HealthResponseStatus(Enum):
    """Overall system status"""

    DEGRADED = "degraded"
    OK = "ok"


@dataclass
class HealthResponse:
    indexer: Indexer
    status: HealthResponseStatus
    """Overall system status"""

    @staticmethod
    def from_dict(obj: Any) -> 'HealthResponse':
        assert isinstance(obj, dict)
        indexer = Indexer.from_dict(obj.get("indexer"))
        status = HealthResponseStatus(obj.get("status"))
        return HealthResponse(indexer, status)

    def to_dict(self) -> dict:
        result: dict = {}
        result["indexer"] = to_class(Indexer, self.indexer)
        result["status"] = to_enum(HealthResponseStatus, self.status)
        return result


class IndexerStatsResponseStatus(Enum):
    """Indexer health status"""

    HEALTHY = "healthy"
    LAGGING = "lagging"
    STALLED = "stalled"


@dataclass
class IndexerStatsResponse:
    network: str
    """Network name from NETWORK environment variable"""

    status: IndexerStatsResponseStatus
    """Indexer health status"""

    avg_poll_duration_ms: int | None = None
    """Average poll duration in milliseconds"""

    chain_tip_ledger: int | None = None
    """Current chain tip ledger (from RPC)"""

    events_indexed_total: int | None = None
    """Cumulative events indexed"""

    events_last_poll: int | None = None
    """Events processed in last poll"""

    lag_ledgers: int | None = None
    """Number of ledgers behind chain tip"""

    last_ledger_indexed: int | None = None
    """Latest indexed ledger sequence"""

    last_poll_at: str | None = None
    """Timestamp of last successful poll"""

    @staticmethod
    def from_dict(obj: Any) -> 'IndexerStatsResponse':
        assert isinstance(obj, dict)
        network = from_str(obj.get("network"))
        status = IndexerStatsResponseStatus(obj.get("status"))
        avg_poll_duration_ms = from_union([from_int, from_none], obj.get("avg_poll_duration_ms"))
        chain_tip_ledger = from_union([from_int, from_none], obj.get("chain_tip_ledger"))
        events_indexed_total = from_union([from_int, from_none], obj.get("events_indexed_total"))
        events_last_poll = from_union([from_int, from_none], obj.get("events_last_poll"))
        lag_ledgers = from_union([from_int, from_none], obj.get("lag_ledgers"))
        last_ledger_indexed = from_union([from_int, from_none], obj.get("last_ledger_indexed"))
        last_poll_at = from_union([from_str, from_none], obj.get("last_poll_at"))
        return IndexerStatsResponse(network, status, avg_poll_duration_ms, chain_tip_ledger, events_indexed_total, events_last_poll, lag_ledgers, last_ledger_indexed, last_poll_at)

    def to_dict(self) -> dict:
        result: dict = {}
        result["network"] = from_str(self.network)
        result["status"] = to_enum(IndexerStatsResponseStatus, self.status)
        if self.avg_poll_duration_ms is not None:
            result["avg_poll_duration_ms"] = from_union([from_int, from_none], self.avg_poll_duration_ms)
        if self.chain_tip_ledger is not None:
            result["chain_tip_ledger"] = from_union([from_int, from_none], self.chain_tip_ledger)
        if self.events_indexed_total is not None:
            result["events_indexed_total"] = from_union([from_int, from_none], self.events_indexed_total)
        if self.events_last_poll is not None:
            result["events_last_poll"] = from_union([from_int, from_none], self.events_last_poll)
        if self.lag_ledgers is not None:
            result["lag_ledgers"] = from_union([from_int, from_none], self.lag_ledgers)
        if self.last_ledger_indexed is not None:
            result["last_ledger_indexed"] = from_union([from_int, from_none], self.last_ledger_indexed)
        if self.last_poll_at is not None:
            result["last_poll_at"] = from_union([from_str, from_none], self.last_poll_at)
        return result


@dataclass
class OpenAPIModels:
    contract_stats: ContractStats | None = None
    contract_stats_response: ContractStatsResponse | None = None
    error_response: ErrorResponse | None = None
    event_list_response: EventListResponse | None = None
    health_response: HealthResponse | None = None
    indexer_stats_response: IndexerStatsResponse | None = None
    soroban_event: SorobanEvent | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'OpenAPIModels':
        assert isinstance(obj, dict)
        contract_stats = from_union([ContractStats.from_dict, from_none], obj.get("ContractStats"))
        contract_stats_response = from_union([ContractStatsResponse.from_dict, from_none], obj.get("ContractStatsResponse"))
        error_response = from_union([ErrorResponse.from_dict, from_none], obj.get("ErrorResponse"))
        event_list_response = from_union([EventListResponse.from_dict, from_none], obj.get("EventListResponse"))
        health_response = from_union([HealthResponse.from_dict, from_none], obj.get("HealthResponse"))
        indexer_stats_response = from_union([IndexerStatsResponse.from_dict, from_none], obj.get("IndexerStatsResponse"))
        soroban_event = from_union([SorobanEvent.from_dict, from_none], obj.get("SorobanEvent"))
        return OpenAPIModels(contract_stats, contract_stats_response, error_response, event_list_response, health_response, indexer_stats_response, soroban_event)

    def to_dict(self) -> dict:
        result: dict = {}
        if self.contract_stats is not None:
            result["ContractStats"] = from_union([lambda x: to_class(ContractStats, x), from_none], self.contract_stats)
        if self.contract_stats_response is not None:
            result["ContractStatsResponse"] = from_union([lambda x: to_class(ContractStatsResponse, x), from_none], self.contract_stats_response)
        if self.error_response is not None:
            result["ErrorResponse"] = from_union([lambda x: to_class(ErrorResponse, x), from_none], self.error_response)
        if self.event_list_response is not None:
            result["EventListResponse"] = from_union([lambda x: to_class(EventListResponse, x), from_none], self.event_list_response)
        if self.health_response is not None:
            result["HealthResponse"] = from_union([lambda x: to_class(HealthResponse, x), from_none], self.health_response)
        if self.indexer_stats_response is not None:
            result["IndexerStatsResponse"] = from_union([lambda x: to_class(IndexerStatsResponse, x), from_none], self.indexer_stats_response)
        if self.soroban_event is not None:
            result["SorobanEvent"] = from_union([lambda x: to_class(SorobanEvent, x), from_none], self.soroban_event)
        return result


def open_api_models_from_dict(s: Any) -> OpenAPIModels:
    return OpenAPIModels.from_dict(s)


def open_api_models_to_dict(x: OpenAPIModels) -> Any:
    return to_class(OpenAPIModels, x)
