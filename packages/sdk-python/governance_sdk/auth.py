"""Authentication client."""
from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

from .client import BaseClient

if TYPE_CHECKING:
    pass


@dataclass
class LoginResponse:
    access_token: str
    refresh_token: str
    expires_in: int
    token_type: str


class AuthClient(BaseClient):
    def login(self, email: str, password: str, tenant_id: str) -> LoginResponse:
        data = self._request("POST", "/v1/auth/login", {
            "email": email, "password": password, "tenant_id": tenant_id,
        })
        resp = LoginResponse(**{k: data[k] for k in LoginResponse.__dataclass_fields__})
        self.set_token(resp.access_token)
        return resp

    def refresh(self, refresh_token: str) -> LoginResponse:
        data = self._request("POST", "/v1/auth/refresh", {"refresh_token": refresh_token})
        resp = LoginResponse(**{k: data[k] for k in LoginResponse.__dataclass_fields__})
        self.set_token(resp.access_token)
        return resp

    def logout(self) -> None:
        self._request("POST", "/v1/auth/logout")
        self._token = None

    def initiate_step_up(self, obligation_token: str) -> None:
        self._request("POST", "/v1/auth/step-up/initiate", {"obligation_token": obligation_token})

    def complete_step_up(self, obligation_token: str, mfa_code: str) -> LoginResponse:
        data = self._request("POST", "/v1/auth/step-up/complete", {
            "obligation_token": obligation_token, "mfa_code": mfa_code,
        })
        return LoginResponse(**{k: data[k] for k in LoginResponse.__dataclass_fields__})
