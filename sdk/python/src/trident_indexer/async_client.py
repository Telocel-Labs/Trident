"""Asynchronous Trident client (asyncio)."""

from __future__ import annotations

import json as _json
from typing import Any, AsyncGenerator, Callable, Coroutine, Optional
from urllib.parse import urlencode

import aiohttp
import websockets

from .errors import TridentApiError
from .types import Network, PaginatedEvents, SorobanEvent


class AsyncTridentClient:
    """Async HTTP + WebSocket client for the Trident Soroban event indexer.

    Use as an async context manager to share a single ``aiohttp.ClientSession``
    across calls, or construct directly and call :meth:`close` when done.

    Args:
        api_url: Base URL of the Trident REST API.
        api_key: API key passed as ``X-API-Key`` on every request.
        network: One of ``"mainnet"``, ``"testnet"``, or ``"futurenet"``.
    """

    def __init__(
        self,
        api_url: str,
        api_key: str,
        network: Network = "testnet",
    ) -> None:
        self._api_url = api_url.rstrip("/")
        self._api_key = api_key
        self._network = network
        self._session: Optional[aiohttp.ClientSession] = None

    async def __aenter__(self) -> "AsyncTridentClient":
        self._session = aiohttp.ClientSession(
            headers={"X-API-Key": self._api_key}
        )
        return self

    async def __aexit__(self, *_: Any) -> None:
        await self.close()

    async def close(self) -> None:
        if self._session and not self._session.closed:
            await self._session.close()
            self._session = None

    # ------------------------------------------------------------------
    # Public methods
    # ------------------------------------------------------------------

    async def query_events(
        self,
        contract_id: Optional[str] = None,
        *,
        topic_0: Optional[str] = None,
        topic_1: Optional[str] = None,
        ledger_from: Optional[int] = None,
        ledger_to: Optional[int] = None,
        cursor: Optional[str] = None,
        limit: int = 50,
        event_type: Optional[str] = None,
    ) -> PaginatedEvents:
        """Query historical Soroban events with optional filtering (async)."""
        params: dict[str, Any] = {"limit": limit}
        if contract_id:
            params["contractId"] = contract_id
        if topic_0:
            params["topic0"] = topic_0
        if topic_1:
            params["topic1"] = topic_1
        if ledger_from is not None:
            params["ledgerFrom"] = ledger_from
        if ledger_to is not None:
            params["ledgerTo"] = ledger_to
        if cursor:
            params["cursor"] = cursor
        if event_type:
            params["event_type"] = event_type

        data = await self._get("/v1/events", params=params)
        return PaginatedEvents(
            events=[SorobanEvent.from_api(e) for e in data.get("events", [])],
            cursor=data.get("next_cursor"),
            has_more=bool(data.get("has_more", False)),
        )

    async def get_event_by_id(self, event_id: str) -> SorobanEvent:
        """Fetch a single event by its UUID (async).

        Raises:
            TridentApiError: with ``code="NOT_FOUND"`` if the event does not exist.
        """
        data = await self._get(f"/v1/events/{event_id}")
        return SorobanEvent.from_api(data["event"])

    async def iter_events(
        self,
        contract_id: str,
        *,
        topic_0: Optional[str] = None,
    ) -> AsyncGenerator[SorobanEvent, None]:
        """Async generator that yields real-time events for a contract via WebSocket.

        Usage::

            async for event in client.iter_events("CABC..."):
                print(event)
        """
        ws_base = (
            self._api_url.replace("https://", "wss://").replace("http://", "ws://")
        )
        qs: dict[str, str] = {"contractId": contract_id}
        if topic_0:
            qs["topic0"] = topic_0
        ws_url = f"{ws_base}/ws?{urlencode(qs)}"

        extra_headers = {"X-API-Key": self._api_key}
        async with websockets.connect(ws_url, additional_headers=extra_headers) as ws:
            async for message in ws:
                try:
                    raw = _json.loads(message)
                    yield SorobanEvent.from_api(raw)
                except Exception:
                    continue

    async def subscribe_to_contract(
        self,
        contract_id: str,
        on_event: Callable[[SorobanEvent], Coroutine[Any, Any, None]],
        *,
        topic_0: Optional[str] = None,
    ) -> None:
        """Subscribe to real-time contract events, calling ``on_event`` for each.

        Runs until the WebSocket connection closes or an exception is raised.
        For a non-blocking version use :meth:`iter_events` in a task.
        """
        async for event in self.iter_events(contract_id, topic_0=topic_0):
            await on_event(event)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    async def _get(self, path: str, params: Optional[dict] = None) -> Any:
        session = self._session or aiohttp.ClientSession(
            headers={"X-API-Key": self._api_key}
        )
        url = self._api_url + path
        try:
            async with session.get(url, params=params, timeout=aiohttp.ClientTimeout(total=30)) as resp:
                body = await resp.text()
                if not resp.ok:
                    raise TridentApiError.from_response(resp.status, body)
                return await resp.json(content_type=None)
        except TridentApiError:
            raise
        except aiohttp.ClientError as exc:
            raise TridentApiError(0, "INTERNAL", f"Network error: {exc}") from exc
        finally:
            if self._session is None:
                await session.close()
