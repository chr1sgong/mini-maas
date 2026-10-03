#!/usr/bin/env python3
"""
使用asyncio异步压测，参考vLLM benchmark_serving.py

压测指标：TTFT、TPOT、吞吐，请求P50/P99耗时

流量模式：支持steay和burst

baseline：确保prefix-len = 0
"""

from __future__ import annotations

import argparse
import asyncio
import subprocess
import sys
import json
import random
from dataclasses import asdict, dataclass
import time
from pathlib import Path

from openai import APIStatusError, AsyncOpenAI


class FatalBenchError(Exception):
    """收到非200响应时抛出,中断整个bench"""


WORDS = (
    "alpha beta gamma delta epsilon zeta eta theta kubernetes inference "
    "latency throughput tensor kernel scheduler cluster operator gateway "
    "prefix cache token stream batch memory compute network storage model"
).split()

@dataclass
class ReqResult:
    t_start: float
    ttft_ms: float | None
    tpot_ms: float | None
    e2e_ms: float
    prompt_tokens: int
    completion_tokens: int
    chunks: int
    error: str | None

def gen_prompt(rng: random.Random, n_tokens: int, prefix: str) -> str:
    # 1个英文词约等于1.3 token
    n_words = max(8, int(n_tokens / 1.3))
    body = " ".join(rng.choice(WORDS) for _ in range(n_words))
    return f"{prefix}{body}\n请用一句话总结上述内容。"

async def one_request(client: AsyncOpenAI, model: str, prompt: str, max_tokens: int) -> ReqResult:
    t0 = time.perf_counter()
    t_first = t_last = None
    chunks = 0
    usage = None
    error = None
    try: 
        stream = await client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            max_tokens=max_tokens,
            temperature=0.0,
            stream=True,
            stream_options={"include_usage": True},  # 最后一帧精确token计数
            extra_body={"ignore_eos": True},
        )
        async for chunk in stream:
            if chunk.usage is not None:
                usage = chunk.usage
            if not chunk.choices:
                continue
            delta = chunk.choices[0].delta.content
            if delta:
                now = time.perf_counter()
                if t_first is None:
                    t_first = now
                t_last = now
                chunks += 1
    except APIStatusError as e:
        # 非200: 打印详细错误并中断bench
        print(
            f"\n[FATAL] 非200响应 status={e.status_code} "
            f"method={e.request.method} url={e.request.url}\n"
            f"body={e.body}",
            flush=True,
        )
        raise FatalBenchError(f"status={e.status_code}") from e
    except Exception as e:
        error = f"{type(e).__name__}: {e}"

    t_end = time.perf_counter()

    ttft_ms = (t_first - t0) * 1000 if t_first else None
    tpot_ms = (t_last - t_first) / (chunks - 1) * 1000 if t_first and t_last and chunks > 1 else None

    return ReqResult(
        t_start=t0,
        ttft_ms = ttft_ms,
        tpot_ms = tpot_ms,
        e2e_ms = (t_end - t0) * 1000,
        prompt_tokens = usage.prompt_tokens if usage else 0,
        completion_tokens = usage.completion_tokens if usage else 0,
        chunks = chunks,
        error = error,
    )

async def run_steady(client: AsyncOpenAI, args, prefix: str, results: list) -> None:
    ddl = time.perf_counter() + args.duration
    rng = random.Random(args.seed)

    async def worker() -> None:
        while time.perf_counter() < ddl:
            prompt = gen_prompt(rng, args.input_tokens, prefix)
            result = await one_request(client, args.model, prompt, args.output_tokens)
            results.append(result)

    await asyncio.gather(*(worker() for _ in range(args.concurrency)))


async def run_burst(client: AsyncOpenAI, args, prefix: str, results: list) -> None:
    rng = random.Random(args.seed)

    batch = await asyncio.gather(*(one_request(client, args.model, gen_prompt(rng, args.input_tokens, prefix), args.output_tokens) for _ in range(args.burst_size)))
    results.extend(batch)

def _run_quiet(cmd: list[str], timeout: float = 5.0) -> str | None:
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return out.stdout.strip() or None
    except Exception:
        return None

def write_run_json(path: Path, args, run_id: str, start_unix: float, end_unix: float, wall: float, summary: dict) -> None:
    git_rev = _run_quiet(["git", "rev-parse", "--short", "HEAD"])
    model_cr = None
    raw = _run_quiet(["kubectl", "-n", "mini-maas", "get", "model", args.model, "-o", "json"])
    if raw:
        try:
            model_cr = json.loads(raw).get("spec")
        except json.JSONDecodeError:
            model_cr = None

    # 网关config.json: rounting.strategy实时读取
    config_snapshot = None
    if args.config_snapshot:
        try:
            config_snapshot = args.config_snapshot.read_text(encoding="utf-8")
        except OSError:
            pass

    meta = {
        "run_id": run_id,
        "argv": sys.argv[1:],
        "git_rev": git_rev,
        "start_unix": start_unix,
        "end_unix": end_unix,
        "wall_s": wall,
        "model_cr_spec": model_cr,
        "config_snapshot": config_snapshot,
        "summary": summary
    }

    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"run 清单已写入: {path}")

def pct(values: list[float], q: float) -> float:
    if not values:
        return float("nan")
    xs = sorted(values)
    k = (len(xs) - 1) * q / 100
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)

def summarize(results: list[ReqResult], wall_s: float) -> dict:
    ok_results = [r for r in results if r.error is None]
    ttft = [r.ttft_ms for r in ok_results if r.ttft_ms is not None]
    tpot = [r.tpot_ms for r in ok_results if r.tpot_ms is not None]

    return {
        "requests": len(results),
        "success_rate": len(ok_results) / max(1, len(results)),
        "ttft_p50_ms": pct(ttft, 50),
        "ttft_p99_ms": pct(ttft, 99),
        "tpot_p50_ms": pct(tpot, 50),
        "tpot_p99_ms": pct(tpot, 99),
        "throughput_tokens_s": sum(r.completion_tokens for r in ok_results) / wall_s if wall_s else 0.0,
        "wall_s": wall_s
    }

def print_summarize(s: dict) -> None:
    print("\n==== 结果 =====\n")
    print(f"请求数 {s['requests']} 成功率 {s['success_rate']:.2%} 墙钟 {s['wall_s']:.2f} 秒")
    print(f"TTFT P50 {s['ttft_p50_ms']:.2f} ms P99 {s['ttft_p99_ms']:.2f} ms")
    print(f"TPOT P50 {s['tpot_p50_ms']:.2f} ms P99 {s['tpot_p99_ms']:.2f} ms")
    print(f"吞吐量 {s['throughput_tokens_s']:.2f} tokens/秒")

# append bench results to the specific markdown file
def append_md(path: Path, args, s: dict) -> None:
    size = args.concurrency if args.mode == "steady" else args.burst_size

    row = (
        f"| {args.mode} | {size} | {args.input_tokens} | {args.output_tokens} | {args.duration}s "
        f"| {s['ttft_p50_ms']:.2f} | {s['ttft_p99_ms']:.2f} "
        f"| {s['tpot_p50_ms']:.2f} | {s['tpot_p99_ms']:.2f} " 
        f"| {s['throughput_tokens_s']:.2f} | {s['success_rate']:.2%} |\n"
    )

    text = path.read_text(encoding="utf-8") if path.exists() else ""
    marker = "<!-- bench results -->"
    if marker not in text:
        text += (
            "\n| 模式 | 并发/突发数 | 输入tokens | 输出tokens | 时长"
            " | TTFT P50 (ms) | TTFT P99 (ms) | TPOT P50 (ms) | TPOT P99 (ms)"
            " | 吞吐 (tokens/s) | 成功率 |\n"
            "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n"
            f"{marker}\n"
        )

    path.write_text(text.replace(marker, row + marker, 1), encoding="utf-8")
    print(f"已追加结果到 {path}")

async def run_benchmark() -> None:
    p = argparse.ArgumentParser(description="mini-maas benchmark (详细口径参考docs/baseline/baseline.md)")
    p.add_argument("--base-url", default="http://localhost:8080/v1")
    p.add_argument("--api-key", default="EMPTY")
    p.add_argument("--model", default="qwen2.5-7b-awq")
    p.add_argument("--mode", choices=["steady", "burst"], default="steady")
    p.add_argument("--concurrency", type=int, default=8, help="steady模式并发数")
    p.add_argument("--duration", type=int, default=60, help="steady模式持续时间（秒）")
    p.add_argument("--burst-size", type=int, default=32, help="burst模式瞬时请求数")
    p.add_argument("--input-tokens", type=int, default=256)
    p.add_argument("--output-tokens", type=int, default=128)
    p.add_argument("--prefix-len", type=int, default=0, help="共享前缀词数（>0, 基线测试不要设置）")
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--out", type=Path, default=None, help="逐个请求的JSONL输出路径")
    p.add_argument("--md", type=Path, default=None, help="汇总行追加到的markdown文件")
    p.add_argument("--run-id", default=None, help="本次运行标识")
    p.add_argument("--run-json", type=Path, default=None, help="run运行清单输出路径")
    p.add_argument("--config-snapshot", type=Path, default=None, help="配置快照路径")
    args = p.parse_args()

    prefix = ""
    if args.prefix_len > 0:
        prefix = " ".join(WORDS[i % len(WORDS)] for i in range(args.prefix_len)) + " "

    client = AsyncOpenAI(base_url=args.base_url, api_key=args.api_key, timeout=300)
    results: list[ReqResult] = []

    start_unix = time.time()
    run_id = args.run_id or (
        time.strftime("%Y%m%d-%H%M%S", time.gmtime(start_unix))
        + f"-{args.mode}"
        + (f"-c{args.concurrency}" if args.mode == "steady" else f"-b{args.burst_size}")
    )

    # 预热
    await one_request(client, args.model, gen_prompt(random.Random(0), 32, prefix), 16)

    t0 = time.perf_counter()
    try:
        if args.mode == "steady":
            await run_steady(client, args, prefix, results)
        else:
            await run_burst(client, args, prefix, results)
    except FatalBenchError:
        print("bench 因非200响应中断,请根据上方 [FATAL] 日志排查", flush=True)
        return

    wall = time.perf_counter() - t0
    end_unix = time.time()

    s = summarize(results, wall)
    print_summarize(s)
    print(f"run_id={run_id} start_unix={start_unix:.3f} end_unix={end_unix:.3f}")

    run_json = args.run_json or (args.out.parent / "run.json" if args.out else None)
    if run_json:
        write_run_json(run_json, args, run_id, start_unix, end_unix, wall, s)
    else:
        print("未指定 --out/--run-json, 未保存结果. 请手动分析结果")

    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        cfg = {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()}
        with args.out.open("w", encoding="utf-8") as f:
            f.write(json.dumps({"config": cfg, "summary": s}, ensure_ascii=False) + "\n")
            for r in results:
                f.write(json.dumps(asdict(r), ensure_ascii=False) + "\n")
        print(f"已将所有请求数据逐条写入：{args.out}")

    if args.md:
        append_md(args.md, args, s)

if __name__ == "__main__":
    asyncio.run(run_benchmark())