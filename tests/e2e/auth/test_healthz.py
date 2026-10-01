import json
import os
import urllib.error
import urllib.request

import pytest

GATEWAY_API = os.environ.get("GATEWAY_API", "http://localhost:8080/v1").rstrip("/")
MODEL = os.environ.get("GATEWAY_MODEL", "qwen2-5-7b")


@pytest.fixture(scope="module")
def api():
    """网关 OpenAI 兼容端点，如 http://localhost:8080/v1。"""
    return GATEWAY_API


@pytest.fixture(scope="module")
def model():
    """被测模型名，需与网关路由表及 key 权限一致。"""
    return MODEL


def test_healthz(api):
    req = urllib.request.Request(f"{api}/healthz", method="GET")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status = resp.status
    except urllib.error.HTTPError as e:
        status = e.code
    assert status == 200, f"status={status}"
