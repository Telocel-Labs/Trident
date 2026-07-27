"""trident-indexer — Python client SDK for the Trident Soroban event indexer."""

from .client import TridentClient
from .async_client import AsyncTridentClient
from .errors import TridentApiError
from .types import SorobanEvent, PaginatedEvents, Network
from .openapi_models_gen import OpenAPIModels, SorobanEvent as OpenAPISorobanEvent, EventListResponse, HealthResponse, IndexerStatsResponse, ContractStats, ContractStatsResponse, ErrorResponse

__all__ = [
    "TridentClient",
    "AsyncTridentClient",
    "TridentApiError",
    "SorobanEvent",
    "PaginatedEvents",
    "Network",
    "OpenAPIModels",
    "OpenAPISorobanEvent",
    "EventListResponse",
    "HealthResponse",
    "IndexerStatsResponse",
    "ContractStats",
    "ContractStatsResponse",
    "ErrorResponse",
]
