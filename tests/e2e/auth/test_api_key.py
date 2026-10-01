import os
import pytest

from tests.e2e.utils import chat_body, error_envelope, post_raw

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


def _assert_401_invalid_api_key(status, resp):
    """统一断言：401 + OpenAI 错误信封 code=invalid_api_key。"""
    env = error_envelope(resp)
    assert status == 401, f"status={status}, want 401"
    assert env is not None, f"响应不是合法 JSON 错误信封: {resp!r}"
    assert env.get("code") == "invalid_api_key", f"code={env.get('code')}"


def test_auth_no_authorization_header(api, model):
    """无 Authorization 头 → 401 invalid_api_key。body 必须合法，否则 400 先于 401。"""
    url = f"{api}/chat/completions"
    body = chat_body(model, max_tokens=1)
    status, _, resp = post_raw(url, body, key=None)
    _assert_401_invalid_api_key(status, resp)


def test_auth_missing_bearer_prefix(api, model):
    """Authorization 无 Bearer 前缀 → 401 invalid_api_key。"""
    url = f"{api}/chat/completions"
    body = chat_body(model, max_tokens=1)
    status, _, resp = post_raw(
        url, body, extra_headers={"Authorization": "sk-whatever"})
    _assert_401_invalid_api_key(status, resp)


def test_auth_wrong_api_key(api, model):
    """格式正确但 key 不存在 → 401 invalid_api_key。"""
    url = f"{api}/chat/completions"
    body = chat_body(model, max_tokens=1)
    status, _, resp = post_raw(url, body, key="sk-wrong")
    _assert_401_invalid_api_key(status, resp)

def test_authz_404(api):
    api_key = "sk-tenant-a-EXAMPLE"
    model = "not_found"
    status, _, resp = post_raw(f"{api}/chat/completions",
                               chat_body(model, max_tokens=1), key=api_key)
    env = error_envelope(resp)
    assert status == 403, f"status={status}, want 403"
    assert env is not None, f"响应不是合法 JSON 错误信封: {resp!r}"
    assert env.get("code") == "model_not_allowed", f"code={env.get('code')}"
