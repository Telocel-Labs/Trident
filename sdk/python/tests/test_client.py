"""Tests for the synchronous TridentClient."""

import json
import pytest
from unittest.mock import MagicMock, patch

from trident_indexer import TridentClient, TridentApiError, SorobanEvent, PaginatedEvents
from tests.conftest import API_URL, API_KEY, RAW_EVENT, LIST_RESPONSE


def make_response(status_code: int, json_body: dict) -> MagicMock:
    resp = MagicMock()
    resp.ok = status_code < 400
    resp.status_code = status_code
    resp.json.return_value = json_body
    resp.text = json.dumps(json_body)
    return resp


def make_client() -> TridentClient:
    return TridentClient(api_url=API_URL, api_key=API_KEY)


class TestQueryEvents:
    def test_returns_paginated_events(self):
        client = make_client()
        with patch.object(client._session, "get", return_value=make_response(200, LIST_RESPONSE)):
            result = client.query_events(contract_id="CABC")

        assert isinstance(result, PaginatedEvents)
        assert len(result.events) == 1
        assert result.cursor == "cursor123"
        assert result.has_more is True

    def test_event_fields_mapped_correctly(self):
        client = make_client()
        with patch.object(client._session, "get", return_value=make_response(200, LIST_RESPONSE)):
            result = client.query_events()

        event = result.events[0]
        assert isinstance(event, SorobanEvent)
        assert event.id == RAW_EVENT["id"]
        assert event.contract_id == RAW_EVENT["contract_id"]
        assert event.ledger_sequence == 100
        assert event.topics == ["transfer"]
        assert event.data == {"amount": 100}  # JSON-decoded from string

    def test_sends_api_key_header(self):
        client = make_client()
        with patch.object(client._session, "get", return_value=make_response(200, LIST_RESPONSE)) as mock_get:
            client.query_events()

        assert client._session.headers.get("X-API-Key") == API_KEY

    def test_passes_optional_filters(self):
        client = make_client()
        with patch.object(client._session, "get", return_value=make_response(200, LIST_RESPONSE)) as mock_get:
            client.query_events(
                contract_id="CABC",
                topic_0="transfer",
                ledger_from=10,
                ledger_to=20,
                limit=100,
            )
        _, kwargs = mock_get.call_args
        params = kwargs["params"]
        assert params["contractId"] == "CABC"
        assert params["topic0"] == "transfer"
        assert params["ledgerFrom"] == 10
        assert params["ledgerTo"] == 20
        assert params["limit"] == 100

    def test_raises_trident_api_error_on_401(self):
        client = make_client()
        error_body = {"error": {"code": "UNAUTHORIZED", "message": "bad key"}}
        with patch.object(client._session, "get", return_value=make_response(401, error_body)):
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events()

        err = exc_info.value
        assert err.status == 401
        assert err.code == "UNAUTHORIZED"

    def test_raises_on_non_json_error(self):
        client = make_client()
        resp = MagicMock()
        resp.ok = False
        resp.status_code = 503
        resp.text = "Service Unavailable"
        with patch.object(client._session, "get", return_value=resp):
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events()

        assert exc_info.value.status == 503
        assert exc_info.value.code == "INTERNAL"


class TestGetEventById:
    def test_returns_soroban_event(self):
        client = make_client()
        with patch.object(client._session, "get", return_value=make_response(200, {"event": RAW_EVENT})):
            event = client.get_event_by_id("550e8400-e29b-41d4-a716-446655440000")

        assert isinstance(event, SorobanEvent)
        assert event.id == RAW_EVENT["id"]

    def test_raises_not_found(self):
        client = make_client()
        error_body = {"error": {"code": "NOT_FOUND", "message": "event not found"}}
        with patch.object(client._session, "get", return_value=make_response(404, error_body)):
            with pytest.raises(TridentApiError) as exc_info:
                client.get_event_by_id("missing-id")

        assert exc_info.value.status == 404
        assert exc_info.value.code == "NOT_FOUND"


class TestSubscribeToContract:
    def test_calls_on_event_for_each_message(self):
        import threading
        import json

        client = make_client()
        received = []

        ws_mock = MagicMock()

        with patch("websocket.WebSocketApp") as MockWS:
            # Capture the on_message callback
            captured_on_message = {}

            def ws_init(url, header=None, on_message=None, **kwargs):
                captured_on_message["fn"] = on_message
                ws_mock.run_forever = MagicMock()
                return ws_mock

            MockWS.side_effect = ws_init

            handle = client.subscribe_to_contract(
                "CABC", on_event=lambda e: received.append(e)
            )

            # Simulate receiving a message
            captured_on_message["fn"](ws_mock, json.dumps(RAW_EVENT))
            assert len(received) == 1
            assert isinstance(received[0], SorobanEvent)
            assert received[0].id == RAW_EVENT["id"]

    def test_close_stops_subscription(self):
        client = make_client()
        ws_mock = MagicMock()
        ws_mock.run_forever = MagicMock()

        with patch("websocket.WebSocketApp", return_value=ws_mock):
            handle = client.subscribe_to_contract("CABC", on_event=lambda e: None)

        handle.close()
        ws_mock.close.assert_called_once()
