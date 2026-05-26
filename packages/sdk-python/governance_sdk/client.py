"""Base HTTP client for the Governance Platform SDK."""
from __future__ import annotations

import json
from typing import Any

import httpx

SDK_VERSION = "1.0.0"


class GovernanceError(Exception):
    """Raised for non-2xx API responses."""

    def __init__(self, code: str, message: str, request_id: str | None = None, status: int | None = None):
        self.code = code
        self.message = message
        self.request_id = request_id
        self.status = status
        detail = f"[{code}] {message}"
        if request_id:
            detail += f" (request_id: {request_id})"
        super().__init__(detail)


class BaseClient:
    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        timeout: float = 30.0,
        http_client: httpx.Client | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._timeout = timeout
        self._http = http_client or httpx.Client(timeout=timeout)

    def set_token(self, token: str) -> None:
        self._token = token

    def _headers(self) -> dict[str, str]:
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": f"governance-sdk-python/{SDK_VERSION}",
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        return headers

    def _request(self, method: str, path: str, body: Any = None) -> Any:
        resp = self._http.request(
            method=method,
            url=self._base_url + path,
            headers=self._headers(),
            content=json.dumps(body).encode() if body is not None else None,
        )
        if resp.status_code >= 400:
            try:
                err = resp.json()
                raise GovernanceError(
                    code=err.get("code", "http_error"),
                    message=err.get("message", resp.text),
                    request_id=err.get("request_id"),
                    status=resp.status_code,
                )
            except (ValueError, KeyError):
                raise GovernanceError("http_error", resp.text, status=resp.status_code)

        if not resp.text:
            return None
        return resp.json()

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> "BaseClient":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()


class AsyncBaseClient:
    """Async variant backed by httpx.AsyncClient."""

    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        timeout: float = 30.0,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._timeout = timeout
        self._http = http_client or httpx.AsyncClient(timeout=timeout)

    def set_token(self, token: str) -> None:
        self._token = token

    def _headers(self) -> dict[str, str]:
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": f"governance-sdk-python/{SDK_VERSION}",
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        return headers

    async def _request(self, method: str, path: str, body: Any = None) -> Any:
        resp = await self._http.request(
            method=method,
            url=self._base_url + path,
            headers=self._headers(),
            content=json.dumps(body).encode() if body is not None else None,
        )
        if resp.status_code >= 400:
            try:
                err = resp.json()
                raise GovernanceError(
                    code=err.get("code", "http_error"),
                    message=err.get("message", resp.text),
                    request_id=err.get("request_id"),
                    status=resp.status_code,
                )
            except (ValueError, KeyError):
                raise GovernanceError("http_error", resp.text, status=resp.status_code)
        if not resp.text:
            return None
        return resp.json()

    async def aclose(self) -> None:
        await self._http.aclose()

    async def __aenter__(self) -> "AsyncBaseClient":
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.aclose()
