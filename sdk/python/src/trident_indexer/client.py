"""Synchronous Trident client."""

from __future__ import annotations

import threading
import time
from typing import Any, Callable, Optional
from urllib.parse import urlencode

import requests
import websocket  # websocket-client

from ._config import redact_key, resolve_api_key, resolve_api_url
from .errors import TridentApiError
from .retry import (
    RetryConfig,
    RetryOverride,
    compute_backoff_seconds,
    is_retryable_status,
    parse_retry_after_seconds,
    resolve_retry_config,
)
from .types import Network, PaginatedEvents, SorobanEvent


class _Subscription:
    """Handle returned by subscribe_to_contract. Call .close() to stop."""

    def __init__(self, ws: websocket.WebSocketApp, thread: threading.Thread) -> None:
        self._ws = ws
        self._thread = thread

    def close(self) -> None:
        self._ws.close()
        self._thread.join(timeout=5)


class TridentClient:
    """Synchronous HTTP + WebSocket client for the Trident Soroban event indexer.

    Args:
        api_url: Base URL of the Trident REST API, e.g. ``"https://api.trident.example.com"``.
            Falls back to the ``TRIDENT_BASE_URL`` environment variable when omitted.
        api_key: API key passed as ``X-API-Key`` on every request. Falls back to
            the ``TRIDENT_API_KEY`` environment variable when omitted.
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
        self._session = requests.Session()
        self._session.headers.update({"X-API-Key": self._api_key})

    def __repr__(self) -> str:  # pragma: no cover
        return (
            f"TridentClient(api_url={self._api_url!r}, "
            f"api_key={redact_key(self._api_key)}, network={self._network!r})"
        )

    # ------------------------------------------------------------------
    # Public methods
    # ------------------------------------------------------------------

    def query_events(
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
        """Query historical Soroban events with optional filtering.

        Results are cursor-paginated. Pass the returned ``cursor`` on the next
        call to fetch the next page.

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

        data = self._get("/v1/events", params=params, retry=retry)
        return PaginatedEvents(
            events=[SorobanEvent.from_api(e) for e in data.get("events", [])],
            cursor=data.get("next_cursor"),
            has_more=bool(data.get("has_more", False)),
        )

    def get_event_by_id(
        self, event_id: str, *, retry: RetryOverride = None
    ) -> SorobanEvent:
        """Fetch a single event by its UUID.

        Args:
            retry: Overrides the client-level retry policy for this call only.

        Raises:
            TridentApiError: with ``code="NOT_FOUND"`` if the event does not exist.
        """
        data = self._get(f"/v1/events/{event_id}", retry=retry)
        return SorobanEvent.from_api(data["event"])

    def subscribe_to_contract(
        self,
        contract_id: str,
        on_event: Callable[[SorobanEvent], None],
        *,
        topic_0: Optional[str] = None,
    ) -> _Subscription:
        """Open a WebSocket subscription to real-time contract events.

        The ``on_event`` callback is invoked on a background thread for each
        event received. Returns a :class:`_Subscription` handle; call
        ``.close()`` to stop the subscription.
        """
        ws_base = (
            self._api_url.replace("https://", "wss://").replace("http://", "ws://")
        )
        qs: dict[str, str] = {"contractId": contract_id}
        if topic_0:
            qs["topic0"] = topic_0
        ws_url = f"{ws_base}/ws?{urlencode(qs)}"

        import json as _json

        def on_message(ws: websocket.WebSocketApp, message: str) -> None:
            try:
                raw = _json.loads(message)
                on_event(SorobanEvent.from_api(raw))
            except Exception:
                pass

        ws_app = websocket.WebSocketApp(
            ws_url,
            header={"X-API-Key": self._api_key},
            on_message=on_message,
        )
        t = threading.Thread(target=ws_app.run_forever, daemon=True)
        t.start()
        return _Subscription(ws_app, t)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _get(
        self,
        path: str,
        params: Optional[dict[str, Any]] = None,
        retry: RetryOverride = None,
    ) -> Any:
        retry_cfg = resolve_retry_config(retry, self._retry)
        max_attempts = retry_cfg.max_attempts if retry_cfg else 1
        url = self._api_url + path
        total_waited = 0.0
        attempt = 0

        while True:
            attempt += 1
            try:
                resp = self._session.get(url, params=params, timeout=30)
            except requests.RequestException as exc:
                if retry_cfg and attempt < max_attempts:
                    wait = compute_backoff_seconds(attempt, retry_cfg)
                    if total_waited + wait <= retry_cfg.max_total_wait:
                        total_waited += wait
                        time.sleep(wait)
                        continue
                code = "RETRY_EXHAUSTED" if attempt > 1 else "INTERNAL"
                raise TridentApiError(
                    0, code, f"Network error: {exc}", attempts=attempt
                ) from exc

            if not resp.ok:
                if (
                    retry_cfg
                    and is_retryable_status(resp.status_code)
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
                        time.sleep(wait)
                        continue
                raise TridentApiError.from_response(
                    resp.status_code, resp.text, attempts=attempt
                )
            return resp.json()
