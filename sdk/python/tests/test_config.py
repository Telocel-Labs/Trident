"""Tests for API key / base URL precedence and redaction."""

import pytest

from trident_indexer import AsyncTridentClient, TridentClient, TridentConfigError
from tests.conftest import API_KEY, API_URL


class TestPrecedence:
    def test_explicit_values_win_over_env(self, monkeypatch):
        monkeypatch.setenv("TRIDENT_API_KEY", "env-key")
        monkeypatch.setenv("TRIDENT_BASE_URL", "https://env.example.com")

        client = TridentClient(api_url=API_URL, api_key=API_KEY)

        assert client._api_key == API_KEY
        assert client._api_url == API_URL

    def test_falls_back_to_env_when_omitted(self, monkeypatch):
        monkeypatch.setenv("TRIDENT_API_KEY", "env-key")
        monkeypatch.setenv("TRIDENT_BASE_URL", "https://env.example.com")

        client = TridentClient()

        assert client._api_key == "env-key"
        assert client._api_url == "https://env.example.com"

    def test_async_client_precedence_matches_sync(self, monkeypatch):
        monkeypatch.setenv("TRIDENT_API_KEY", "env-key")
        monkeypatch.setenv("TRIDENT_BASE_URL", "https://env.example.com")

        client = AsyncTridentClient(api_key=API_KEY)

        assert client._api_key == API_KEY
        assert client._api_url == "https://env.example.com"


class TestMissingConfig:
    def test_missing_api_key_raises_clear_error(self, monkeypatch):
        monkeypatch.delenv("TRIDENT_API_KEY", raising=False)

        with pytest.raises(TridentConfigError, match="API key is required"):
            TridentClient(api_url=API_URL)

    def test_missing_api_url_raises_clear_error(self, monkeypatch):
        monkeypatch.delenv("TRIDENT_BASE_URL", raising=False)

        with pytest.raises(TridentConfigError, match="api_url is required"):
            TridentClient(api_key=API_KEY)


class TestRedaction:
    def test_repr_never_contains_raw_key(self):
        client = TridentClient(api_url=API_URL, api_key=API_KEY)
        assert API_KEY not in repr(client)
        assert "***" in repr(client)

    def test_async_repr_never_contains_raw_key(self):
        client = AsyncTridentClient(api_url=API_URL, api_key=API_KEY)
        assert API_KEY not in repr(client)
        assert "***" in repr(client)
