"""
governance-sdk — AI-Native Authorization & Retrieval Governance Platform SDK for Python.

Quick start::

    from governance_sdk import GovernanceClient

    client = GovernanceClient(base_url="https://api.platform.io")
    resp = client.auth.login(email="alice@example.com", password="...", tenant_id="t-123")
    decision = client.pdp.decide(
        tenant_id="t-123",
        subject=SubjectContext(user_id="u-1", roles=["analyst"]),
        resource=ResourceContext(type="table", id="customers"),
        action="select",
    )
    print(decision.decision)  # "allow" or "deny"
"""

from .client import GovernanceError
from .auth import AuthClient, LoginResponse
from .pdp import PDPClient, DecideResponse, SubjectContext, ResourceContext, Obligation
from .webhooks import WebhookClient, verify_signature


class GovernanceClient:
    """Unified SDK entry point."""

    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        import httpx
        self._http = httpx.Client(timeout=timeout)
        self.auth = AuthClient(base_url, token=token, http_client=self._http)
        self.pdp = PDPClient(base_url, token=token, http_client=self._http)
        self.webhooks = WebhookClient(base_url, token=token, http_client=self._http)

    def set_token(self, token: str) -> None:
        for client in (self.auth, self.pdp, self.webhooks):
            client.set_token(token)

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> "GovernanceClient":
        return self

    def __exit__(self, *args: object) -> None:
        self.close()


__all__ = [
    "GovernanceClient",
    "GovernanceError",
    "LoginResponse",
    "DecideResponse",
    "SubjectContext",
    "ResourceContext",
    "Obligation",
    "verify_signature",
]
