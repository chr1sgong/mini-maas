#!/usr/bin/env python3

"""
使用官方OpenAI SDK直连vLLM，验证流式输出，并统计TTFT
"""

import sys
import time

from openai import OpenAI

BASE_URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8000/v1"

client = OpenAI(base_url=BASE_URL, api_key="EMPTY")

model = client.models.list().data[0].id
print(f"model:{model}")
# 预热
resp = client.chat.completions.create(
    model=model,
    messages=[
        {"role": "user", "content": "用三句话介绍 Kubernates, 每句不超过 20 字。"}
    ],
    max_tokens=128,
    temperature=0.7,
    stream=True
)

chunks = 0
for c in resp:
    delta = c.choices[0].delta.content
    if delta:
        chunks += 1
        print(delta, end="", flush=True)

t0 = time.perf_counter()
resp = client.chat.completions.create(
    model=model,
    messages=[
        {"role": "user", "content": "用三句话介绍 Kubernates, 每句不超过 20 字。"}
    ],
    max_tokens=128,
    temperature=0.7,
    stream=True
)

ttft = None
chunks = 0
for c in resp:
    delta = c.choices[0].delta.content
    if delta:
        if ttft is None:
            ttft = time.perf_counter() - t0
        chunks += 1
        print(delta, end="", flush=True)

total = time.perf_counter() - t0
print(f"\n---\nTTFT: {ttft * 1000:.0f} ms | chunks: {chunks} | total: {total:.2f} s")
assert chunks > 1, "FAIL: 未收到流式分片(响应被整段返回)"