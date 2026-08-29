"""Asynchronous Trident client (asyncio)."""

from __future__ import annotations

import asyncio
import json as _json
from typing import Any, AsyncGenerator, Callable, Coroutine, Optional
from urllib.parse import urlencode

import aiohttp
import websockets

from ._config import redact_key, resolve_api_key, resolve_api_url
from .errors import TridentApiError
from .retry import (
    RetryOverride,
    compute_backoff_seconds,
    is_retryable_status,
    parse_retry_after_seconds,
    resolve_retry_config,
)
from .types import Network, PaginatedEvents, SorobanEvent


class AsyncTridentClient:
    """Async HTTP + WebSocket client for the Trident Soroban event indexer.

    Use as an async context manager to share a single ``aiohttp.ClientSession``
    across calls, or construct directly and call :meth:`close` when done.

    Args:
        api_url: Base URL of the Trident REST API. Falls back to the
            ``TRIDENT_BASE_URL`` environment variable when omitted.
        api_key: API key passed as ``X-API-Key`` on every request. Falls back
            to the ``TRIDENT_API_KEY`` environment variable when omitted.
        network: One of ``"mainnet"``, ``"testnet"``, or ``"futurenet"``.
        retry: Retry policy applied to idempotent (GET) requests. Honours
            ``Retry-After`` on 429/503 responses, falling back to exponential
            backoff with jitter otherwise. Pass ``False`` to disable retries
            for this client, or a :class:`~trident_indexer.retry.RetryConfig`
            to customize the policy. Defaults to
            :data:`~trident_indexer.retry.DEFAULT_RETRY_CONFIG`.
    """

    def __init__(
        self,
        api_url: Optional[str] = None,
        api_key: Optional[str] = None,
        network: Network = "testnet",
        retry: RetryOverride = None,
    ) -> None:
        self._api_url = resolve_api_url(api_url)
        self._api_key = resolve_api_key(api_key)
        self._network = network
        self._retry = retry
        self._session: Optional[aiohttp.ClientSession] = None

    def __repr__(self) -> str:  # pragma: no cover
        return (
            f"AsyncTridentClient(api_url={self._api_url!r}, "
            f"api_key={redact_key(self._api_key)}, network={self._network!r})"
        )

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
        retry: RetryOverride = None,
    ) -> PaginatedEvents:
        """Query historical Soroban events with optional filtering (async).

        Args:
            retry: Overrides the client-level retry policy for this call only.
        """
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

        data = await self._get("/v1/events", params=params, retry=retry)
        return PaginatedEvents(
            events=[SorobanEvent.from_api(e) for e in data.get("events", [])],
            cursor=data.get("next_cursor"),
            has_more=bool(data.get("has_more", False)),
        )

    async def get_event_by_id(
        self, event_id: str, *, retry: RetryOverride = None
    ) -> SorobanEvent:
        """Fetch a single event by its UUID (async).

        Args:
            retry: Overrides the client-level retry policy for this call only.

        Raises:
            TridentApiError: with ``code="NOT_FOUND"`` if the event does not exist.
        """
        data = await self._get(f"/v1/events/{event_id}", retry=retry)
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

    async def _get(
        self,
        path: str,
        params: Optional[dict[str, Any]] = None,
        retry: RetryOverride = None,
    ) -> Any:
        retry_cfg = resolve_retry_config(retry, self._retry)
        max_attempts = retry_cfg.max_attempts if retry_cfg else 1
        session = self._session or aiohttp.ClientSession(
            headers={"X-API-Key": self._api_key}
        )
        url = self._api_url + path
        total_waited = 0.0
        attempt = 0
        try:
            while True:
                attempt += 1
                try:
                    async with session.get(
                        url, params=params, timeout=aiohttp.ClientTimeout(total=30)
                    ) as resp:
                        body = await resp.text()
                        if not resp.ok:
                            if (
                                retry_cfg
                                and is_retryable_status(resp.status)
                                and attempt < max_attempts
                            ):
                                retry_after = parse_retry_after_seconds(
                                    resp.headers.get("Retry-After")
                                )
                                wait = (
                                    retry_after
                                    if retry_after is not None
                                    else compute_backoff_seconds(attempt, retry_cfg)
                                )
                                if total_waited + wait <= retry_cfg.max_total_wait:
                                    total_waited += wait
                                    await asyncio.sleep(wait)
                                    continue
                            raise TridentApiError.from_response(
                                resp.status, body, attempts=attempt
                            )
                        return await resp.json(content_type=None)
                except TridentApiError:
                    raise
                except aiohttp.ClientError as exc:
                    if retry_cfg and attempt < max_attempts:
                        wait = compute_backoff_seconds(attempt, retry_cfg)
                        if total_waited + wait <= retry_cfg.max_total_wait:
                            total_waited += wait
                            await asyncio.sleep(wait)
                            continue
                    code = "RETRY_EXHAUSTED" if attempt > 1 else "INTERNAL"
                    raise TridentApiError(
                        0, code, f"Network error: {exc}", attempts=attempt
                    ) from exc
        finally:
            if self._session is None:
                await session.close()
