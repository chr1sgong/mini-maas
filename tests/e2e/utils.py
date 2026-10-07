import json
import urllib.request
import urllib.error

def chat_body(model, max_tokens, stream=False, prompt="hi", ignore_eos=False, stop=None):
    body = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "stream": stream,
    }
    if ignore_eos:  # vLLM 扩展字段：强制生成满 max_tokens，输出长度确定（TPM 断言的前提）
        body["ignore_eos"] = True
    if stop:
        body["stop"] = stop
    return body

def post_raw(url, body, key="UNSET", timeout=180, extra_headers=None):
    """原始 POST，返回 (状态码, 响应头dict, 响应体str)。key=None 表示完全不带 Authorization。"""
    req = urllib.request.Request(
        url, data=json.dumps(body).encode(), method="POST",
        headers={"Content-Type": "application/json"},
    )
    if key != "UNSET":
        req.add_header("Authorization", f"Bearer {key}")
    for k, v in (extra_headers or {}).items():
        req.remove_header("Authorization")
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, dict(resp.headers), resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode()

def error_envelope(body_str):
    """解析 OpenAI 错误信封，解析失败返回 None。"""
    try:
        return json.loads(body_str).get("error", {})
    except json.JSONDecodeError:
        return None