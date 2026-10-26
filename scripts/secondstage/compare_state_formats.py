#!/usr/bin/env python3
"""Compare numeric and textual candidate-state artifacts."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any, Mapping, Sequence

import evaluate_deepseek as ev


LABELS = {
    "baseline": "first-stage blend",
    "jev": "JEV",
    "jev_stage1": "JEV stage 1",
    "tree": "local tree",
    "numeric": "DeepSeek numeric state",
    "text": "DeepSeek text state",
    "numeric_two_stage": "numeric two-stage",
    "text_two_stage": "text two-stage",
}
STAGE1_COLUMNS = (("Recall@95", "recall@95"), ("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10"))
STAGE2_COLUMNS = (("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10"), ("Hit@30", "hit@30"), ("MaxHigh@30", "max_high@30"))
PAIR_METRICS = ("hit@10", "ndcg@10", "max_high@10", "close3@10")


def load_comparison(path: Path) -> Mapping[str, Any]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ev.InputError(f"cannot read comparison {path}: {exc}") from exc
    if not isinstance(document, Mapping):
        raise ev.InputError(f"comparison must be an object: {path}")
    return document


def section_metrics(
    numeric: Mapping[str, Any],
    text: Mapping[str, Any],
    section: str,
    numeric_key: str,
    text_key: str,
    dates: Sequence[str],
    reference_keys: Sequence[str],
) -> dict[str, Any]:
    try:
        numeric_rows = numeric[section]["per_date"]
        text_rows = text[section]["per_date"]
    except (KeyError, TypeError) as exc:
        raise ev.InputError(f"comparison is missing {section}") from exc
    per_date: dict[str, dict[str, Mapping[str, float]]] = {}
    for date in dates:
        if date not in numeric_rows or date not in text_rows:
            raise ev.InputError(f"comparison is missing {section} for {date}")
        numeric_row = numeric_rows[date]
        text_row = text_rows[date]
        if numeric_key not in numeric_row or text_key not in text_row:
            raise ev.InputError(f"comparison is missing {section} ranker for {date}")
        row: dict[str, Mapping[str, float]] = {}
        for key in reference_keys:
            if key in numeric_row:
                row[key] = numeric_row[key]
        row["numeric"] = numeric_row[numeric_key]
        row["text"] = text_row[text_key]
        per_date[date] = row
    rankers = [key for key in (*reference_keys, "numeric", "text") if key in next(iter(per_date.values()))]
    return {
        "aggregate": ev.aggregate(per_date, dates, rankers),
        "per_date": per_date,
        "pairwise": {
            key: ev.bootstrap_pair(per_date, dates, "text", "numeric", key)
            for key in PAIR_METRICS
        },
    }


def table(title: str, aggregate: Mapping[str, Mapping[str, float]], rankers: Sequence[str], columns: Sequence[tuple[str, str]]) -> list[str]:
    lines = [f"### {title}", "", "| 排序器 | " + " | ".join(label for label, _ in columns) + " |", "|---|" + "---|" * len(columns)]
    for ranker in rankers:
        values = [ev.format_metric(key, float(aggregate[ranker][key])) for _, key in columns]
        lines.append(f"| {LABELS.get(ranker, ranker)} | " + " | ".join(values) + " |")
    return lines


def build_markdown(result: Mapping[str, Any], dates: Sequence[str]) -> str:
    lines = [
        "# State 表达方式对比：精确数值 vs 文字分档",
        "",
        f"- 测试日：{', '.join(dates)}（{len(dates)} 日）",
        "- 两路使用相同候选、5 人组、三种选项顺序、同一 DeepSeek 模型、温度 0、reasoning_effort=none。",
        "- numeric：原始压缩数值；text：按当日候选池计算的七档分位文字（very low / low / low-to-mid / mid / mid-to-high / high / very high）。",
        "- 两路只改变 state 表达，不改变候选池、组顺序、评分 prompt 目标和 artifact schema。",
        "- 标签是未来 3 个交易日最大最高价涨幅和收盘涨幅，不代表可成交收益或成功概率。",
        "",
    ]
    if "stage1" in result:
        stage1_rankers = tuple(
            key for key in ("baseline", "jev_stage1", "numeric", "text")
            if key in result["stage1"]["aggregate"]
        )
        lines.extend(table(
            "初筛阶段：全池重排", result["stage1"]["aggregate"],
            stage1_rankers, STAGE1_COLUMNS,
        ))
        lines.append("")
    stage2_rankers = tuple(
        key for key in ("baseline", "tree", "jev", "numeric", "text")
        if key in result["stage2"]["aggregate"]
    )
    lines.extend(table(
        "精排阶段：固定 first-stage Top95", result["stage2"]["aggregate"],
        stage2_rankers, STAGE2_COLUMNS,
    ))
    lines.append("")
    if "deepseek_pool" in result:
        e2e_rankers = tuple(
            key for key in ("baseline", "numeric_two_stage", "text_two_stage")
            if key in result["deepseek_pool"]["aggregate"]
        )
        lines.extend(table(
            "端到端：各自初筛 Top95 + 精排", result["deepseek_pool"]["aggregate"],
            e2e_rankers,
            (("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10")),
        ))
        lines.append("")
    lines.append("### 配对 bootstrap：文字 state − 数值 state")
    lines.append("")
    lines.append("| 阶段 | 指标 | 均值 | 95% CI | 文字胜/负/平 |")
    lines.append("|---|---|---:|---:|---:|")
    for section, title in (("stage1", "初筛"), ("stage2", "精排"), ("deepseek_pool", "端到端")):
        if section not in result:
            continue
        for key in PAIR_METRICS:
            item = result[section]["pairwise"][key]
            lines.append(
                f"| {title} | {key} | {ev.format_metric(key, item['mean'])} | "
                f"[{ev.format_metric(key, item['low'])}, {ev.format_metric(key, item['high'])}] | "
                f"{item['wins']}/{item['losses']}/{item['ties']} |"
            )
    return "\n".join(lines) + "\n"


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--numeric", required=True, type=Path)
    parser.add_argument("--text", required=True, type=Path)
    parser.add_argument("--out-dir", required=True, type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    numeric = load_comparison(args.numeric)
    text = load_comparison(args.text)
    try:
        dates = [str(value) for value in numeric["protocol"]["dates"]]
        text_dates = [str(value) for value in text["protocol"]["dates"]]
    except (KeyError, TypeError) as exc:
        raise ev.InputError("comparison protocol dates are missing") from exc
    if dates != text_dates:
        raise ev.InputError("numeric and text comparisons use different dates")
    result: dict[str, Any] = {
        "protocol": {
            "dates": dates,
            "numeric": "compact rounded numeric state",
            "text": "within-day seven-level quantile labels",
            "group_size": 5,
            "permutations": 3,
            "reasoning_effort": "none",
            "text_labels": ["very low", "low", "low-to-mid", "mid", "mid-to-high", "high", "very high"],
        },
        "stage2": section_metrics(numeric, text, "stage2", "deepseek", "deepseek", dates, ("baseline", "tree", "jev")),
    }
    if "stage1" in numeric and "stage1" in text:
        result["stage1"] = section_metrics(numeric, text, "stage1", "deepseek_stage1", "deepseek_stage1", dates, ("baseline", "jev_stage1"))
    if "deepseek_pool" in numeric and "deepseek_pool" in text:
        e2e = section_metrics(numeric, text, "deepseek_pool", "deepseek_two_stage", "deepseek_two_stage", dates, ("baseline",))
        numeric_rows = numeric["deepseek_pool"]["per_date"]
        text_rows = text["deepseek_pool"]["per_date"]
        for date in dates:
            e2e["per_date"][date]["numeric_two_stage"] = numeric_rows[date]["deepseek_two_stage"]
            e2e["per_date"][date]["text_two_stage"] = text_rows[date]["deepseek_two_stage"]
        rankers = ("baseline", "numeric_two_stage", "text_two_stage")
        e2e["aggregate"] = ev.aggregate(e2e["per_date"], dates, rankers)
        e2e["pairwise"] = {
            key: ev.bootstrap_pair(e2e["per_date"], dates, "text_two_stage", "numeric_two_stage", key)
            for key in PAIR_METRICS
        }
        result["deepseek_pool"] = e2e
    args.out_dir.mkdir(parents=True, exist_ok=True)
    ev.write_json_atomic(args.out_dir / "comparison.json", result)
    markdown = build_markdown(result, dates)
    (args.out_dir / "comparison.md").write_text(markdown, encoding="utf-8")
    print(markdown)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
