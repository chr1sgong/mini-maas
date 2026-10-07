"""
前置：
  1. 用 gateway/gateway.e2e.json 启动网关（模型名需与路由表一致），
     例：./gateway --config gateway.e2e.json
  2. 依赖：openai SDK（必须用官方 SDK）

用法：
  python3 -m pytest tests/e2e/settle/test_settle.py
"""
import os
import time

import pytest

from tests.e2e.utils import chat_body, post_raw

GATEWAY_API = os.environ.get("GATEWAY_API", "http://localhost:8080/v1").rstrip("/")
MODEL = os.environ.get("GATEWAY_MODEL", "qwen2-5-7b")
API_KEY = os.environ.get("GATEWAY_API_KEY", "sk-tenant-a-EXAMPLE")

API_KEY_A = os.environ.get("GATEWAY_API_KEY_A", "sk-tenant-a-EXAMPLE")
API_KEY_B = os.environ.get("GATEWAY_API_KEY_B", "sk-tenant-b-EXAMPLE")
API_KEY_C = os.environ.get("GATEWAY_API_KEY_C", "sk-tenant-c-EXAMPLE")
API_KEY_D = os.environ.get("GATEWAY_API_KEY_D", "sk-tenant-d-EXAMPLE")

@pytest.fixture(scope="module")
def api():
    """网关 OpenAI 兼容端点，如 http://localhost:8080/v1。"""
    return GATEWAY_API


@pytest.fixture(scope="module")
def model():
    """被测模型名，需与网关路由表及 key 权限一致。"""
    return MODEL

@pytest.fixture(scope="module")
def key():
    """用于测试的 API Key。"""
    return API_KEY

@pytest.fixture(scope="module")
def key_a():
    """用于测试的 API Key A。"""
    return API_KEY_A

@pytest.fixture(scope="module")
def key_b():
    """用于测试的 API Key B。"""
    return API_KEY_B

@pytest.fixture(scope="module")
def key_c():
    """用于测试的 API Key C。"""
    return API_KEY_C

@pytest.fixture(scope="module")
def key_d():
    """用于测试的 API Key D。"""
    return API_KEY_D

def test_settlement(api, key_c, model):
    url = f"{api}/chat/completions"
    # 请求1（流式）：max_tokens=100 但要求极短回答，实际生成 ≈ 几个 token。
    # 预估扣减 ≈ 25+100=125；实际 ≈ 15；回填 ≈ 110 → 余额回到 ≈ 585
    s1, _, _ = post_raw(url, chat_body(model, max_tokens=100, stream=True,
                                       prompt="只回复一个字：好", stop=["\n"]), key=key_c)
    time.sleep(1.5)  # 等旁路结算完成（异步）
    # 请求2：预估 ≈ 25+480=505。若已回填（余额≈585）→ 过；若没回填（余额≈475）→ 429
    s2, _, _ = post_raw(url, chat_body(model, max_tokens=480, ignore_eos=True,
                                       prompt="写个长故事"), key=key_c)
    ok = s1 == 200 and s2 == 200

# ---------- #8 include_usage 注入（官方 SDK） ----------

def test_include_usage(api, key_a, model):
    from openai import OpenAI
    time.sleep(2)  # 让 key A 的 QPS 桶从 #5 恢复
    client = OpenAI(base_url=api, api_key=key_a)
    # 客户端不传 stream_options：若收到 usage chunk，必然是网关注入的
    chunks, usage = 0, None
    for chunk in client.chat.completions.create(
            model=model, messages=[{"role": "user", "content": "数到5"}],
            max_tokens=32, stream=True):
        chunks += 1
        if getattr(chunk, "usage", None):
            usage = chunk.usage
    ok = chunks > 1 and usage is not None and usage.total_tokens > 0