#!/usr/bin/env python3
"""export_run.py — 把一次压测的 Prometheus 数据落盘

输入：run.json（bench 的墙钟锚点）或手工 --start/--end；queries.yaml 查询清单。
输出：<out-dir>/metrics/<name>.csv（长表 ts,metric,value）+ summary.json（整窗聚合）。

注意事项：
- step 对齐 scrape 间隔：queries.yaml 每条自带 step，别在脚本里"聪明"地覆盖
- series 缺失不 fatal：空 CSV + warning，实验日一定会有条目查空
- 原始值落盘：不做单位换算/重采样，加工交给 render_charts.py 与报告

前置：kubectl -n observability port-forward svc/prometheus 9090
"""
from __future__ import annotations

import argparse
import csv
import json
import shutil
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

import yaml  # 仅用于 manifest 解析；网络/CSV 全 stdlib


def parse_step(step: str) -> int:
    # queries.yaml 里 step 写作 "5s"/"15s"，Prometheus API 吃秒数（字符串亦可）
    return int(step.rstrip("s"))


def prom_get(prom_url: str, path: str, params: dict) -> list[dict]:
    url = f"{prom_url}/api/v1/{path}?{urllib.parse.urlencode(params)}"
    with urllib.request.urlopen(url, timeout=30) as resp:
        body = json.loads(resp.read())
    if body.get("status") != "success":
        # 把 Prometheus 的错误原样抛出来：PromQL 写错时这是唯一的线索
        raise RuntimeError(f"{path} 失败: {body.get('errorType')}: {body.get('error')}  query={params.get('query')}")
    return body["data"]["result"]


def export_curve(prom_url: str, entry: dict, start: float, end: float, csv_path: Path) -> int:
    result = prom_get(prom_url, "query_range", {
        "query": entry["promql"],
        "start": f"{start:.3f}",
        "end": f"{end:.3f}",
        "step": parse_step(entry["step"]),
    })
    n_points = 0
    with csv_path.open("w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["ts", "metric", "value"])
        for series in result:
            # label 集原样存 JSON：长表格式，下游 pandas/肉眼都友好
            label = json.dumps(series["metric"], sort_keys=True, ensure_ascii=False)
            for ts, val in series["values"]:
                w.writerow([f"{ts:.3f}", label, val])
                n_points += 1
    return n_points


def export_aggregate(prom_url: str, entry: dict, end: float, window_s: int) -> list[dict]:
    # {window} 占位符换成运行时长；instant query 取 end 时刻（此刻 scrape 已 settled）
    promql = entry["promql"].replace("{window}", f"{window_s}s")
    result = prom_get(prom_url, "query", {"query": promql, "time": f"{end:.3f}"})
    rows = []
    for series in result:
        _ts, val = series["value"]
        rows.append({"metric": series["metric"], "value": float(val)})
    return rows


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--run-json", type=Path, default=None, help="bench 产出的 run 清单（提供 start/end/run_id）")
    p.add_argument("--start", type=float, default=None, help="手工窗口起点 unix 秒（无 run.json 时用，如补导出）")
    p.add_argument("--end", type=float, default=None, help="手工窗口终点 unix 秒")
    p.add_argument("--queries", type=Path, default=Path(__file__).with_name("queries.yaml"))
    p.add_argument("--prom-url", default="http://localhost:9090")
    p.add_argument("--out-dir", type=Path, default=None, help="缺省 docs/experiments/<run_id>/")
    p.add_argument("--pad", type=float, default=60, help="curve 窗口前后各加的秒数（看爬坡/回落，D11 看扩容延迟）")
    args = p.parse_args()

    run_id = time.strftime("%Y%m%d-%H%M%S", time.gmtime())
    if args.run_json:
        meta = json.loads(args.run_json.read_text(encoding="utf-8"))
        start, end = float(meta["start_unix"]), float(meta["end_unix"])
        run_id = meta.get("run_id", run_id)
    elif args.start and args.end:
        start, end = args.start, args.end
    else:
        p.error("需要 --run-json 或 --start/--end")

    out_dir = args.out_dir or Path("docs/experiments") / run_id
    metrics_dir = out_dir / "metrics"
    metrics_dir.mkdir(parents=True, exist_ok=True)

    queries = yaml.safe_load(args.queries.read_text(encoding="utf-8"))
    window_s = int(end - start)
    summary: dict[str, list[dict]] = {}
    for entry in queries:
        name = entry["name"]
        if entry["use"] == "curve":
            n = export_curve(args.prom_url, entry, start - args.pad, end + args.pad,
                             metrics_dir / f"{name}.csv")
            if n == 0:
                print(f"[warn] {name}: 窗口内无数据，已写空 CSV", file=sys.stderr)
            else:
                print(f"{name}: {n} 点")
        else:
            rows = export_aggregate(args.prom_url, entry, end, window_s)
            summary[name] = rows
            if not rows:
                print(f"[warn] {name}: 聚合无结果", file=sys.stderr)

    (out_dir / "summary.json").write_text(
        json.dumps({"run_id": run_id, "start_unix": start, "end_unix": end,
                    "window_s": window_s, "aggregates": summary},
                   ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if args.run_json:
        dst = out_dir / "run.json"
        # --out 指进实验目录时 run.json 已在其中，同路径拷贝会报 SameFileError
        if args.run_json.resolve() != dst.resolve():
            shutil.copy(args.run_json, dst)
    print(f"\n导出完成：{out_dir}")
    print(f"下一步：python scripts/render_charts.py --exp-dir {out_dir}")


if __name__ == "__main__":
    main()
