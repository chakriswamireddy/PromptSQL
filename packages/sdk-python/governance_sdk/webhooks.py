"""Webhook utilities and client."""
from __future__ import annotations

import hashlib
import hmac

from .client import BaseClient


def verify_signature(payload: bytes | str, signature: str, secret: str) -> bool:
    """
    Verify the HMAC-SHA256 webhook signature.

    :param payload:   Raw request body (bytes or str).
    :param signature: Value of X-Governance-Signature header, e.g. "sha256=<hex>".
    :param secret:    Webhook signing secret.
    :returns:         True if the signature is valid.
    """
    if not signature.startswith("sha256="):
        return False
    expected_hex = signature[len("sha256="):]
    if isinstance(payload, str):
        payload = payload.encode()
    mac = hmac.new(secret.encode(), payload, hashlib.sha256).hexdigest()
    return hmac.compare_digest(mac, expected_hex)


class WebhookClient(BaseClient):
    def create(self, tenant_id: str, url: str, event_types: list[str]) -> dict:
        return self._request("POST", f"/v1/admin/{tenant_id}/webhooks", {
            "url": url, "event_types": event_types,
        })

    def list(self, tenant_id: str) -> list[dict]:
        data = self._request("GET", f"/v1/admin/{tenant_id}/webhooks")
        return data.get("subscriptions", [])

    def delete(self, tenant_id: str, subscription_id: str) -> None:
        self._request("DELETE", f"/v1/admin/{tenant_id}/webhooks/{subscription_id}")
