#!/usr/bin/env bash
# 并发 8 个请求
for i in $(seq 1 8); do
  curl -sD- http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-tenant-a-EXAMPLE" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5-7b-awq",
    "messages": [{"role": "user", "content": "请说一下你对短视频的理解"}]
  }' &
done; wait