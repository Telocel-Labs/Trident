"""Tests for the async AsyncTridentClient."""

import json
import pytest
from unittest.mock import AsyncMock, MagicMock, patch, PropertyMock

from trident_indexer import AsyncTridentClient, TridentApiError, SorobanEvent, PaginatedEvents
from tests.conftest import API_URL, API_KEY, RAW_EVENT, LIST_RESPONSE


def make_client() -> AsyncTridentClient:
    return AsyncTridentClient(api_url=API_URL, api_key=API_KEY)


def make_aiohttp_response(status: int, body: dict) -> MagicMock:
    resp = MagicMock()
    resp.ok = status < 400
    resp.status = status
    resp.text = AsyncMock(return_value=json.dumps(body))
    resp.json = AsyncMock(return_value=body)
    resp.__aenter__ = AsyncMock(return_value=resp)
    resp.__aexit__ = AsyncMock(return_value=False)
    return resp


class TestAsyncQueryEvents:
    @pytest.mark.asyncio
    async def test_returns_paginated_events(self):
        client = make_client()
        resp = make_aiohttp_response(200, LIST_RESPONSE)

        with patch("aiohttp.ClientSession.get", return_value=resp):
            async with client:
                result = await client.query_events(contract_id="CABC")

        assert isinstance(result, PaginatedEvents)
        assert len(result.events) == 1
        assert result.cursor == "cursor123"
        assert result.has_more is True

    @pytest.mark.asyncio
    async def test_event_fields_mapped_correctly(self):
        client = make_client()
        resp = make_aiohttp_response(200, LIST_RESPONSE)

        with patch("aiohttp.ClientSession.get", return_value=resp):
            async with client:
                result = await client.query_events()

        event = result.events[0]
        assert isinstance(event, SorobanEvent)
        assert event.id == RAW_EVENT["id"]
        assert event.data == {"amount": 100}

    @pytest.mark.asyncio
    async def test_raises_on_401(self):
        client = make_client()
        error_body = {"error": {"code": "UNAUTHORIZED", "message": "bad key"}}
        resp = make_aiohttp_response(401, error_body)

        with patch("aiohttp.ClientSession.get", return_value=resp):
            async with client:
                with pytest.raises(TridentApiError) as exc_info:
                    await client.query_events()

        assert exc_info.value.status == 401
        assert exc_info.value.code == "UNAUTHORIZED"


class TestAsyncGetEventById:
    @pytest.mark.asyncio
    async def test_returns_soroban_event(self):
        client = make_client()
        resp = make_aiohttp_response(200, {"event": RAW_EVENT})

        with patch("aiohttp.ClientSession.get", return_value=resp):
            async with client:
                event = await client.get_event_by_id(RAW_EVENT["id"])

        assert isinstance(event, SorobanEvent)
        assert event.id == RAW_EVENT["id"]

    @pytest.mark.asyncio
    async def test_raises_not_found(self):
        client = make_client()
        error_body = {"error": {"code": "NOT_FOUND", "message": "not found"}}
        resp = make_aiohttp_response(404, error_body)

        with patch("aiohttp.ClientSession.get", return_value=resp):
            async with client:
                with pytest.raises(TridentApiError) as exc_info:
                    await client.get_event_by_id("missing")

        assert exc_info.value.code == "NOT_FOUND"


class TestIterEvents:
    @pytest.mark.asyncio
    async def test_yields_events_from_websocket(self):
        client = make_client()
        received = []

        async def fake_ws(*args, **kwargs):
            class FakeWS:
                async def __aenter__(self):
                    return self

                async def __aexit__(self, *_):
                    pass

                def __aiter__(self):
                    return self

                async def __anext__(self):
                    if not received:
                        return json.dumps(RAW_EVENT)
                    raise StopAsyncIteration

            return FakeWS()

        with patch("websockets.connect", side_effect=fake_ws):
            async for event in client.iter_events("CABC"):
                received.append(event)

        assert len(received) == 1
        assert isinstance(received[0], SorobanEvent)
        assert received[0].id == RAW_EVENT["id"]


class TestTridentApiError:
    def test_from_response_parses_structured_error(self):
        body = json.dumps({"error": {"code": "NOT_FOUND", "message": "not found", "field": "id"}})
        err = TridentApiError.from_response(404, body)
        assert err.status == 404
        assert err.code == "NOT_FOUND"
        assert err.field == "id"
        assert str(err) == "not found"

    def test_from_response_falls_back_on_non_json(self):
        err = TridentApiError.from_response(503, "Service Unavailable")
        assert err.status == 503
        assert err.code == "INTERNAL"
        assert "Service Unavailable" in str(err)

    def test_from_response_handles_empty_body(self):
        err = TridentApiError.from_response(500, "")
        assert err.code == "INTERNAL"
