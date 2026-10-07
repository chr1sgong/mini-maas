"""
前置：
  1. 用 gateway/gateway.e2e.json 启动网关（模型名需与路由表一致），
     例：./gateway --config gateway.e2e.json
  2. 依赖：openai SDK（必须用官方 SDK）

用法：
  python3 -m pytest tests/e2e/ratelimit/test_limit.py
"""

from concurrent.futures import ThreadPoolExecutor
import os
import time

import pytest

from tests.e2e.utils import chat_body, error_envelope, post_raw

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



def test_no_quota(api, key, model):
    url = f"{api}/chat/completions"
    body = chat_body(model, max_tokens=1)
    # 先并发打 50 个假 key（全部应 401），再立刻并发 5 个真 key。
    # 若 401 消耗了 QPS 额度（限流跑在鉴权外层），真 key 会立刻 429。
    with ThreadPoolExecutor(50) as ex:
        fakes = list(ex.map(lambda _: post_raw(url, body, key="sk-fake"), range(50)))
    fake_ok = all(s == 401 for s, _, _ in fakes)
    with ThreadPoolExecutor(5) as ex:
        reals = list(ex.map(lambda _: post_raw(url, body, key=key), range(5)))
    real_codes = [s for s, _, _ in reals]
    assert fake_ok and real_codes.count(200) == 5
    time.sleep(2)  # 让 key 的 QPS 桶回满，供 #5 用

def test_qps_limit(api, key_a, model):
    url = f"{api}/chat/completions"
    body = chat_body(model, max_tokens=1)
    with ThreadPoolExecutor(20) as ex:
        results = list(ex.map(lambda _: post_raw(url, body, key=key_a), range(20)))
    passed = [r for r in results if r[0] == 200]
    rejected = [r for r in results if r[0] == 429]
    retry_after_ok = all("Retry-After" in h for _, h, _ in rejected)
    type_ok = all((error_envelope(b) or {}).get("type") == "requests" for _, _, b in rejected)
    # 桶容量 5、补充 5/s：并发 20 个应过约 5 个（测试期间补充可略多）
    ok = 4 <= len(passed) <= 10 and len(rejected) >= 8 and retry_after_ok and type_ok
    time.sleep(1.5)
    with ThreadPoolExecutor(3) as ex:
        after = list(ex.map(lambda _: post_raw(url, body, key=key_a), range(3)))
    assert all(s == 200 for s, _, _ in after)



def test_tpm_limit(api, key_d, model):
    url = f"{api}/chat/completions"
    # TPM=600（补充 10/s）。ignore_eos 保证输出长度确定，断言不被结算回填干扰。
    # 请求1：预估扣减 ≈ 25+300=325 ≤ 600 → 过；实际 ≈ 315，回填 ≈ 10，余额 ≈ 285
    s1, _, _ = post_raw(url, chat_body(model, max_tokens=300, ignore_eos=True,
                                       prompt="写个长故事"), key=key_d)
    # 请求2：预估 325 > 余额 285 → 429 type=tokens
    s2, h2, b2 = post_raw(url, chat_body(model, max_tokens=300, ignore_eos=True,
                                         prompt="再写个长故事"), key=key_d)
    env2 = error_envelope(b2)
    # 请求3：小请求（预估 ≈ 25+100=125 ≤ 285）→ 过，证明桶没被搞死
    s3, _, _ = post_raw(url, chat_body(model, max_tokens=100, ignore_eos=True), key=key_d)
    assert (s1 == 200 and s2 == 429
          and env2 and env2.get("type") == "tokens" and "Retry-After" in h2
          and s3 == 200)
