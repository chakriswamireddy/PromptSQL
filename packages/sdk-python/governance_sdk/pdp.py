"""Policy Decision Point client."""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .client import BaseClient


@dataclass
class SubjectContext:
    user_id: str
    roles: list[str]
    attrs: dict[str, str] = field(default_factory=dict)


@dataclass
class ResourceContext:
    type: str
    id: str
    attributes: dict[str, str] = field(default_factory=dict)


@dataclass
class Obligation:
    type: str
    payload: dict[str, Any] = field(default_factory=dict)


@dataclass
class DecideResponse:
    decision: str  # "allow" | "deny"
    obligations: list[Obligation] = field(default_factory=list)
    column_masks: dict[str, str] = field(default_factory=dict)
    row_filter: str | None = None
    reason: str | None = None
    trace_id: str | None = None


class PDPClient(BaseClient):
    def decide(
        self,
        tenant_id: str,
        subject: SubjectContext,
        resource: ResourceContext,
        action: str,
        environment: dict[str, Any] | None = None,
    ) -> DecideResponse:
        body = {
            "tenant_id": tenant_id,
            "subject": {"user_id": subject.user_id, "roles": subject.roles, "attrs": subject.attrs},
            "resource": {"type": resource.type, "id": resource.id, "attributes": resource.attributes},
            "action": action,
        }
        if environment:
            body["environment"] = environment
        data = self._request("POST", "/v1/pdp/decide", body)
        return DecideResponse(
            decision=data["decision"],
            obligations=[Obligation(**o) for o in data.get("obligations", [])],
            column_masks=data.get("column_masks", {}),
            row_filter=data.get("row_filter"),
            reason=data.get("reason"),
            trace_id=data.get("trace_id"),
        )

    def bulk_decide(
        self,
        tenant_id: str,
        subject: SubjectContext,
        requests: list[dict[str, Any]],
    ) -> list[DecideResponse]:
        data = self._request("POST", "/v1/pdp/bulk-decide", {
            "tenant_id": tenant_id,
            "subject": {"user_id": subject.user_id, "roles": subject.roles},
            "requests": requests,
        })
        return [
            DecideResponse(decision=r["decision"],
                           column_masks=r.get("column_masks", {}),
                           row_filter=r.get("row_filter"))
            for r in data.get("results", [])
        ]

    def explain(
        self,
        tenant_id: str,
        subject: SubjectContext,
        resource: ResourceContext,
        action: str,
    ) -> str:
        data = self._request("POST", "/v1/pdp/explain", {
            "tenant_id": tenant_id,
            "subject": {"user_id": subject.user_id, "roles": subject.roles},
            "resource": {"type": resource.type, "id": resource.id},
            "action": action,
        })
        return data.get("explanation", "")
