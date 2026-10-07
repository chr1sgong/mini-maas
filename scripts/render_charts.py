#!/usr/bin/env python3
"""render_charts.py — 实验目录的 CSV → 四张固定曲线图

四张图与 Grafana 面板六行同构但面向报告：latency / throughput / queue / kv_cache。
图的职责是"让人 10 秒看懂这次实验"，不是交互探索——交互探索回 Grafana。

用法：python scripts/render_charts.py --exp-dir docs/experiments/<run_id>
"""
from __future__ import annotations

import argparse
import csv
import json
from collections import defaultdict
from pathlib import Path

import matplotlib

matplotlib.use("Agg")  # 无显示环境（SSH/CI）也能出图，必须在 pyplot import 前设置
import matplotlib.pyplot as plt

# 每张图的组成：(csv 名前缀列表, 标题, y 轴, 是否 ×1000 显示为 ms)
# 延迟曲线源数据是秒，图上统一画 ms——报告里人读 ms，CSV 里保留秒的原始值
CHARTS = {
    "latency.png": {
        "left": (["engine_ttft_p50", "engine_ttft_p99", "gateway_ttft_p50", "gateway_ttft_p99"],
                 "TTFT (engine vs gateway)", "ms"),
        "right": (["engine_tpot_p50", "engine_tpot_p99", "gateway_tpot_p50", "gateway_tpot_p99"],
                  "TPOT (engine vs gateway)", "ms"),
    },
    "throughput.png": {
        "left": (["throughput_gen", "throughput_prompt"], "token throughput", "tok/s"),
        "right": (["gateway_rps"], "gateway rps by code", "req/s"),
    },
    "queue.png": {
        "left": (["queue_running", "queue_waiting"], "vLLM queue depth", "requests"),
        "right": (["gateway_inflight"], "gateway inflight by endpoint", "requests"),
    },
    "kv_cache.png": {
        "left": (["kv_cache_perc"], "KV cache usage", "perc"),
        "right": (["replicas_actual"], "actual replicas (scraped instances)", "count"),
    },
}


def load_series(metrics_dir: Path, names: list[str]) -> dict[str, tuple[list[float], list[float]]]:
    """读 CSV 长表 → {图例: (ts 列表, value 列表)}；缺文件/空文件安静跳过"""
    series: dict[str, tuple[list[float], list[float]]] = {}
    for name in names:
        path = metrics_dir / f"{name}.csv"
        if not path.exists():
            continue
        by_label: dict[str, tuple[list[float], list[float]]] = defaultdict(lambda: ([], []))
        with path.open(encoding="utf-8") as f:
            for row in csv.DictReader(f):
                label = json.loads(row["metric"])
                # 图例只保留有区分度的标签，否则单 series 时图例是一长串无用 JSON
                short = {k: v for k, v in label.items()
                         if k in ("instance", "endpoint", "reason", "code", "model_name")}
                key = name if not short else f"{name} {json.dumps(short, ensure_ascii=False)}"
                ts_list, val_list = by_label[key]
                ts_list.append(float(row["ts"]))
                val_list.append(float(row["value"]))
        series.update(by_label)
    return series


def plot_panel(ax, series: dict, title: str, ylabel: str, t0: float) -> None:
    for label, (ts, vals) in sorted(series.items()):
        scale = 1000.0 if ylabel == "ms" else 1.0
        ax.plot([t - t0 for t in ts], [v * scale for v in vals], label=label, linewidth=1.2)
    ax.set_title(title, fontsize=10)
    ax.set_ylabel(ylabel)
    ax.set_xlabel("s since window start")
    ax.grid(alpha=0.3)
    if series:
        ax.legend(fontsize=7, loc="best")


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--exp-dir", type=Path, required=True, help="实验目录（含 metrics/*.csv）")
    args = p.parse_args()
    metrics_dir = args.exp_dir / "metrics"
    charts_dir = args.exp_dir / "charts"
    charts_dir.mkdir(parents=True, exist_ok=True)

    # 所有面板共用同一时间零点：窗口起点（含 padding），跨图对齐肉眼可查
    all_csv = list(metrics_dir.glob("*.csv"))
    t0 = min((float(row["ts"]) for path in all_csv for row in [next(csv.DictReader(path.open(encoding="utf-8")), None)] if row),
             default=0.0)

    for filename, panels in CHARTS.items():
        fig, axes = plt.subplots(1, 2, figsize=(13, 4))
        for ax, key in zip(axes, ("left", "right")):
            names, title, ylabel = panels[key]
            plot_panel(ax, load_series(metrics_dir, names), title, ylabel, t0)
        fig.tight_layout()
        fig.savefig(charts_dir / filename, dpi=120)
        plt.close(fig)
        print(f"已生成 {charts_dir / filename}")


if __name__ == "__main__":
    main()
