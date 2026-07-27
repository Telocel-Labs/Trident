"""Synchronous Trident client."""

from __future__ import annotations

import json as _json
import random
import threading
import time
from typing import Any, Callable, Optional
from urllib.parse import urlencode

import requests
import websocket  # websocket-client

from .errors import TridentApiError
from .types import Network, PaginatedEvents, SorobanEvent

_INITIAL_BACKOFF = 0.5
_MAX_BACKOFF = 30.0
_JITTER_FACTOR = 0.2


class _Subscription:
    """Handle returned by subscribe_to_contract. Call .close() to stop."""

    def __init__(self, stop_event: threading.Event, thread: threading.Thread) -> None:
        self._stop = stop_event
        self._thread = thread

    def close(self) -> None:
        self._stop.set()
        self._thread.join(timeout=10)


class TridentClient:
    """Synchronous HTTP + WebSocket client for the Trident Soroban event indexer.

    Args:
        api_url: Base URL of the Trident REST API, e.g. ``"https://api.trident.example.com"``.
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
        self._session = requests.Session()
        self._session.headers.update({"X-API-Key": api_key})

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
    ) -> PaginatedEvents:
        """Query historical Soroban events with optional filtering.

        Results are cursor-paginated. Pass the returned ``cursor`` on the next
        call to fetch the next page.
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

        data = self._get("/v1/events", params=params)
        return PaginatedEvents(
            events=[SorobanEvent.from_api(e) for e in data.get("events", [])],
            cursor=data.get("next_cursor"),
            has_more=bool(data.get("has_more", False)),
        )

    def get_event_by_id(self, event_id: str) -> SorobanEvent:
        """Fetch a single event by its UUID.

        Raises:
            TridentApiError: with ``code="NOT_FOUND"`` if the event does not exist.
        """
        data = self._get(f"/v1/events/{event_id}")
        return SorobanEvent.from_api(data["event"])

    def subscribe_to_contract(
        self,
        contract_id: str,
        on_event: Callable[[SorobanEvent], None],
        *,
        topic_0: Optional[str] = None,
        on_error: Optional[Callable[[Exception], None]] = None,
        on_connected: Optional[Callable[[], None]] = None,
        on_disconnected: Optional[Callable[[], None]] = None,
        on_resumed: Optional[Callable[[str], None]] = None,
    ) -> _Subscription:
        """Open a WebSocket subscription to real-time contract events.

        Reconnects with exponential backoff + jitter on disconnect, resuming
        from the last received event id via the ``cursor`` query param so no
        events are lost within the server retention window.

        The callbacks are invoked on the background thread.
        Returns a :class:`_Subscription` handle; call ``.close()`` to stop.
        """
        ws_base = self._api_url.replace("https://", "wss://").replace("http://", "ws://")
        stop_event = threading.Event()

        def _build_url(last_event_id: Optional[str]) -> str:
            qs: dict[str, str] = {"contractId": contract_id}
            if topic_0:
                qs["topic0"] = topic_0
            if last_event_id:
                qs["cursor"] = last_event_id
            return f"{ws_base}/ws?{urlencode(qs)}"

        def _run() -> None:
            backoff = _INITIAL_BACKOFF
            last_event_id: Optional[str] = None

            while not stop_event.is_set():
                is_resume = last_event_id is not None
                ws_url = _build_url(last_event_id)
                connected = threading.Event()

                def on_open(ws: websocket.WebSocketApp) -> None:
                    nonlocal backoff
                    backoff = _INITIAL_BACKOFF
                    connected.set()
                    if is_resume and last_event_id:
                        if on_resumed:
                            on_resumed(last_event_id)
                    else:
                        if on_connected:
                            on_connected()

                def on_message(ws: websocket.WebSocketApp, message: str) -> None:
                    nonlocal last_event_id
                    try:
                        raw = _json.loads(message)
                        event = SorobanEvent.from_api(raw)
                        if event.id:
                            last_event_id = event.id
                        on_event(event)
                    except Exception as exc:
                        if on_error:
                            on_error(exc)

                def on_ws_error(ws: websocket.WebSocketApp, error: Exception) -> None:
                    if on_error:
                        on_error(error)

                def on_close(ws: websocket.WebSocketApp, close_status_code: Any, close_msg: Any) -> None:
                    if on_disconnected:
                        on_disconnected()

                ws_app = websocket.WebSocketApp(
                    ws_url,
                    header={"X-API-Key": self._api_key},
                    on_open=on_open,
                    on_message=on_message,
                    on_error=on_ws_error,
                    on_close=on_close,
                )
                ws_app.run_forever()

                if stop_event.is_set():
                    break

                jitter = random.uniform(0, backoff * _JITTER_FACTOR)
                sleep_time = min(backoff + jitter, _MAX_BACKOFF)
                stop_event.wait(timeout=sleep_time)
                backoff = min(backoff * 2, _MAX_BACKOFF)

        t = threading.Thread(target=_run, daemon=True)
        t.start()
        return _Subscription(stop_event, t)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _get(self, path: str, params: Optional[dict] = None) -> Any:
        url = self._api_url + path
        try:
            resp = self._session.get(url, params=params, timeout=30)
        except requests.RequestException as exc:
            raise TridentApiError(0, "INTERNAL", f"Network error: {exc}") from exc
        if not resp.ok:
            raise TridentApiError.from_response(resp.status_code, resp.text)
        return resp.json()
