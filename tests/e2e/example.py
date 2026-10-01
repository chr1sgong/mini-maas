import json
import os
import statistics
import time
import urllib.request

import pytest

from tests.e2e.utils import chat_body

GATEWAY_API = os.environ.get("GATEWAY_API", "http://localhost:8080/v1").rstrip("/")
DIRECT_API = os.environ.get("DIRECT_API", "http://10.42.0.26:8000/v1").rstrip("/")  # ip:port使用pod的ip:port
MODEL = os.environ.get("GATEWAY_MODEL", "qwen2-5-7b")
API_KEY = os.environ.get("GATEWAY_API_KEY", "sk-tenant-a-EXAMPLE")


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
def direct_api():
    """直连 OpenAI 兼容端点，如 http://10.42.0.26:8080/v1。"""
    return DIRECT_API

def test_happy_path(api, key, model):
    from openai import OpenAI
    client = OpenAI(base_url=api, api_key=key)
    ok = True
    resp = client.chat.completions.create(
        model=model, messages=[{"role": "user", "content": "hi"}],
        max_tokens=8, stream=False)
    text = resp.choices[0].message.content
    ok = ok and resp.usage is not None and text is not None
    chunks = 0
    for chunk in client.chat.completions.create(
            model=model, messages=[{"role": "user", "content": "数到5"}],
            max_tokens=32, stream=True):
        chunks += 1
    ok = ok and chunks > 1
    assert ok, f"test_2_happy_path failed, stream chunks={chunks}"

def measure_ttft(url, body, key=None, samples=10):
    latencies = []
    for _ in range(samples):
        req = urllib.request.Request(
            url, data=json.dumps(body).encode(), method="POST",
            headers={"Content-Type": "application/json"})
        if key:
            req.add_header("Authorization", f"Bearer {key}")
        start = time.perf_counter()
        with urllib.request.urlopen(req, timeout=60) as resp:
            first = None
            for line in resp:
                if line.startswith(b"data:") and b"[DONE]" not in line:
                    first = time.perf_counter()
                    break
        latencies.append((first - start) * 1000)
        time.sleep(0.25)  # 低于 QPS 限额
    return statistics.median(latencies)


def test_ttft_tax(api, direct_api, key, model):
    body = chat_body(model, max_tokens=8, stream=True, prompt="hi")
    via_gw = measure_ttft(f"{api}/chat/completions", body, key=key)
    direct = measure_ttft(f"{direct_api}/chat/completions", body, key=None)
    tax = via_gw - direct
    print(f"经网关 P50={via_gw:.1f}ms 直连 P50={direct:.1f}ms 税={tax:.1f}ms")
    assert tax > 0