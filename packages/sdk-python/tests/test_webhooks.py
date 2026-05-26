"""Unit tests for webhook signature verification."""
import hashlib
import hmac

import pytest

from governance_sdk.webhooks import verify_signature


def _sign(payload: bytes, secret: str) -> str:
    mac = hmac.new(secret.encode(), payload, hashlib.sha256).hexdigest()
    return f"sha256={mac}"


PAYLOAD = b'{"event":"policy.created"}'
SECRET = "test-secret-abc123"


def test_valid_signature():
    sig = _sign(PAYLOAD, SECRET)
    assert verify_signature(PAYLOAD, sig, SECRET) is True


def test_tampered_payload():
    sig = _sign(PAYLOAD, SECRET)
    assert verify_signature(PAYLOAD + b"tampered", sig, SECRET) is False


def test_wrong_secret():
    sig = _sign(PAYLOAD, "wrong-secret")
    assert verify_signature(PAYLOAD, sig, SECRET) is False


def test_bad_format():
    assert verify_signature(PAYLOAD, "md5=notvalid", SECRET) is False


def test_str_payload():
    payload_str = '{"event":"policy.created"}'
    sig = _sign(payload_str.encode(), SECRET)
    assert verify_signature(payload_str, sig, SECRET) is True
