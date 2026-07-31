"""Tests for retry-with-backoff behaviour (sync and async clients)."""

import json
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from trident_indexer import AsyncTridentClient, TridentApiError, TridentClient
from trident_indexer.retry import RetryConfig
from tests.conftest import API_URL, API_KEY, LIST_RESPONSE


def make_response(status_code: int, json_body: dict, retry_after: str = None) -> MagicMock:
    resp = MagicMock()
    resp.ok = status_code < 400
    resp.status_code = status_code
    resp.json.return_value = json_body
    resp.text = json.dumps(json_body)
    resp.headers = {"Retry-After": retry_after} if retry_after else {}
    return resp


def make_sync_client(**retry_kwargs) -> TridentClient:
    return TridentClient(
        api_url=API_URL,
        api_key=API_KEY,
        retry=RetryConfig(jitter=False, **retry_kwargs),
    )


class TestSyncRetry:
    def test_succeeds_after_n_transient_503s(self):
        client = make_sync_client(max_attempts=3, base_delay=0.001)
        responses = [
            make_response(503, {"error": {"code": "INTERNAL", "message": "down"}}),
            make_response(503, {"error": {"code": "INTERNAL", "message": "down"}}),
            make_response(200, LIST_RESPONSE),
        ]
        with patch.object(client._session, "get", side_effect=responses) as mock_get, \
                patch("trident_indexer.client.time.sleep") as mock_sleep:
            result = client.query_events()

        assert result.cursor == "cursor123"
        assert mock_get.call_count == 3
        assert mock_sleep.call_count == 2

    def test_honours_retry_after_header_on_429(self):
        client = make_sync_client(max_attempts=3, base_delay=100.0)
        responses = [
            make_response(
                429,
                {"error": {"code": "RATE_LIMITED", "message": "slow down"}},
                retry_after="2",
            ),
            make_response(200, LIST_RESPONSE),
        ]
        with patch.object(client._session, "get", side_effect=responses), \
                patch("trident_indexer.client.time.sleep") as mock_sleep:
            client.query_events()

        # Retry-After (2s) must be honoured instead of the 100s base backoff.
        mock_sleep.assert_called_once_with(2.0)

    def test_gives_up_after_max_attempts_and_surfaces_typed_error(self):
        client = make_sync_client(max_attempts=3, base_delay=0.001)
        response = make_response(503, {"error": {"code": "INTERNAL", "message": "still down"}})
        with patch.object(client._session, "get", return_value=response) as mock_get, \
                patch("trident_indexer.client.time.sleep"):
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events()

        assert exc_info.value.status == 503
        assert exc_info.value.attempts == 3
        assert mock_get.call_count == 3

    def test_does_not_retry_non_retryable_status(self):
        client = make_sync_client(max_attempts=5, base_delay=0.001)
        response = make_response(401, {"error": {"code": "UNAUTHORIZED", "message": "bad key"}})
        with patch.object(client._session, "get", return_value=response) as mock_get, \
                patch("trident_indexer.client.time.sleep") as mock_sleep:
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events()

        assert exc_info.value.attempts == 1
        assert mock_get.call_count == 1
        mock_sleep.assert_not_called()

    def test_retries_disabled_at_client_level(self):
        client = TridentClient(api_url=API_URL, api_key=API_KEY, retry=False)
        response = make_response(503, {"error": {"code": "INTERNAL", "message": "down"}})
        with patch.object(client._session, "get", return_value=response) as mock_get:
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events()

        assert exc_info.value.attempts == 1
        assert mock_get.call_count == 1

    def test_per_call_override_disables_retries(self):
        client = make_sync_client(max_attempts=5, base_delay=0.001)
        response = make_response(503, {"error": {"code": "INTERNAL", "message": "down"}})
        with patch.object(client._session, "get", return_value=response) as mock_get:
            with pytest.raises(TridentApiError) as exc_info:
                client.query_events(retry=False)

        assert exc_info.value.attempts == 1
        assert mock_get.call_count == 1

    def test_applies_to_get_event_by_id(self):
        client = make_sync_client(max_attempts=3, base_delay=0.001)
        responses = [
            make_response(503, {"error": {"code": "INTERNAL", "message": "down"}}),
            make_response(200, {"event": {**LIST_RESPONSE["events"][0]}}),
        ]
        with patch.object(client._session, "get", side_effect=responses) as mock_get, \
                patch("trident_indexer.client.time.sleep"):
            event = client.get_event_by_id(LIST_RESPONSE["events"][0]["id"])

        assert event.id == LIST_RESPONSE["events"][0]["id"]
        assert mock_get.call_count == 2


def make_aiohttp_response(status: int, body: dict, retry_after: str = None) -> MagicMock:
    resp = MagicMock()
    resp.ok = status < 400
    resp.status = status
    resp.text = AsyncMock(return_value=json.dumps(body))
    resp.json = AsyncMock(return_value=body)
    resp.headers = {"Retry-After": retry_after} if retry_after else {}
    resp.__aenter__ = AsyncMock(return_value=resp)
    resp.__aexit__ = AsyncMock(return_value=False)
    return resp


def make_async_client(**retry_kwargs) -> AsyncTridentClient:
    return AsyncTridentClient(
        api_url=API_URL,
        api_key=API_KEY,
        retry=RetryConfig(jitter=False, **retry_kwargs),
    )


class TestAsyncRetry:
    @pytest.mark.asyncio
    async def test_succeeds_after_n_transient_503s(self):
        client = make_async_client(max_attempts=3, base_delay=0.001)
        responses = [
            make_aiohttp_response(503, {"error": {"code": "INTERNAL", "message": "down"}}),
            make_aiohttp_response(503, {"error": {"code": "INTERNAL", "message": "down"}}),
            make_aiohttp_response(200, LIST_RESPONSE),
        ]
        with patch("aiohttp.ClientSession.get", side_effect=responses) as mock_get, \
                patch("trident_indexer.async_client.asyncio.sleep", new=AsyncMock()) as mock_sleep:
            async with client:
                result = await client.query_events()

        assert result.cursor == "cursor123"
        assert mock_get.call_count == 3
        assert mock_sleep.call_count == 2

    @pytest.mark.asyncio
    async def test_honours_retry_after_header_on_429(self):
        client = make_async_client(max_attempts=3, base_delay=100.0)
        responses = [
            make_aiohttp_response(
                429,
                {"error": {"code": "RATE_LIMITED", "message": "slow down"}},
                retry_after="1.5",
            ),
            make_aiohttp_response(200, LIST_RESPONSE),
        ]
        with patch("aiohttp.ClientSession.get", side_effect=responses), \
                patch("trident_indexer.async_client.asyncio.sleep", new=AsyncMock()) as mock_sleep:
            async with client:
                await client.query_events()

        mock_sleep.assert_called_once_with(1.5)

    @pytest.mark.asyncio
    async def test_gives_up_after_max_attempts_and_surfaces_typed_error(self):
        client = make_async_client(max_attempts=3, base_delay=0.001)
        response = make_aiohttp_response(503, {"error": {"code": "INTERNAL", "message": "still down"}})
        with patch("aiohttp.ClientSession.get", return_value=response) as mock_get, \
                patch("trident_indexer.async_client.asyncio.sleep", new=AsyncMock()):
            async with client:
                with pytest.raises(TridentApiError) as exc_info:
                    await client.query_events()

        assert exc_info.value.status == 503
        assert exc_info.value.attempts == 3
        assert mock_get.call_count == 3

    @pytest.mark.asyncio
    async def test_does_not_retry_non_retryable_status(self):
        client = make_async_client(max_attempts=5, base_delay=0.001)
        response = make_aiohttp_response(401, {"error": {"code": "UNAUTHORIZED", "message": "bad key"}})
        with patch("aiohttp.ClientSession.get", return_value=response) as mock_get, \
                patch("trident_indexer.async_client.asyncio.sleep", new=AsyncMock()) as mock_sleep:
            async with client:
                with pytest.raises(TridentApiError) as exc_info:
                    await client.query_events()

        assert exc_info.value.attempts == 1
        assert mock_get.call_count == 1
        mock_sleep.assert_not_called()

    @pytest.mark.asyncio
    async def test_retries_disabled_at_client_level(self):
        client = AsyncTridentClient(api_url=API_URL, api_key=API_KEY, retry=False)
        response = make_aiohttp_response(503, {"error": {"code": "INTERNAL", "message": "down"}})
        with patch("aiohttp.ClientSession.get", return_value=response) as mock_get:
            async with client:
                with pytest.raises(TridentApiError) as exc_info:
                    await client.query_events()

        assert exc_info.value.attempts == 1
        assert mock_get.call_count == 1
