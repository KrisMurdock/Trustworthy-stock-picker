#!/usr/bin/env python3
"""Compare DeepSeek two-stage scores with the local tree and JEV rankers."""

from __future__ import annotations

import argparse
import json
import math
import os
import random
import statistics
import tempfile
from collections import defaultdict
from pathlib import Path
from typing import Any, Mapping, Sequence


GROUP_SIZE = 5
TREE_GROUP_SIZE = 15
DEFAULT_TEST_DATES = (
    "2026-09-24", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-08",
    "2026-10-09", "2026-10-12", "2026-10-13", "2026-10-14",
)
LABELS = {
    "baseline": "first-stage blend",
    "deepseek_stage1": "DeepSeek stage 1",
    "jev_stage1": "JEV stage 1",
    "tree": "local tree",
    "jev": "JEV",
    "deepseek": "DeepSeek stage 2",
    "deepseek_50": "first-stage 50% + DeepSeek 50%",
    "deepseek_jev_40": "first-stage 20% + DeepSeek 40% + JEV 40%",
    "deepseek_tree_40": "first-stage 20% + DeepSeek 40% + tree 40%",
    "deepseek_two_stage": "DeepSeek two-stage (20% + 80%)",
}
METRICS = ("hit@10", "ndcg@10", "max_high@10", "close3@10", "hit@30", "ndcg@30", "max_high@30", "close3@30")


class InputError(ValueError):
    pass


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json_atomic(path: Path, payload: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def percentile(values: Sequence[float]) -> list[float]:
    if not values:
        return []
    order = sorted(range(len(values)), key=lambda index: (values[index], index))
    result = [0.0] * len(values)
    if len(values) == 1:
        result[order[0]] = 0.5
        return result
    denominator = len(values) - 1
    start = 0
    while start < len(order):
        end = start
        while end + 1 < len(order) and values[order[end + 1]] == values[order[start]]:
            end += 1
        value = ((start + end) / 2) / denominator
        for position in range(start, end + 1):
            result[order[position]] = value
        start = end + 1
    return result


def group_percentiles(candidates: Sequence[Mapping[str, Any]], scores: Mapping[str, float]) -> dict[str, float]:
    result: dict[str, float] = {}
    for start in range(0, len(candidates), GROUP_SIZE):
        group = candidates[start : start + GROUP_SIZE]
        missing = [candidate["code"] for candidate in group if candidate["code"] not in scores]
        if missing:
            raise InputError(f"missing scores for {', '.join(missing[:5])}")
        values = [float(scores[candidate["code"]]) for candidate in group]
        for candidate, value in zip(group, percentile(values)):
            result[str(candidate["code"])] = value
    return result


def first_stage_percentiles(candidates: Sequence[Mapping[str, Any]]) -> dict[str, float]:
    values = [float(candidate["blend_score"]) for candidate in candidates]
    return {str(candidate["code"]): value for candidate, value in zip(candidates, percentile(values))}


def score_artifact(path: Path, as_of: str) -> dict[str, float]:
    document = load_json(path)
    if document.get("schema_version") != 1 or document.get("as_of") != as_of:
        raise InputError(f"invalid score artifact: {path}")
    scores = document.get("scores")
    if not isinstance(scores, Mapping) or not scores:
        raise InputError(f"score artifact has no scores: {path}")
    result: dict[str, float] = {}
    for code, value in scores.items():
        score = float(value["adjacent15"])
        if not math.isfinite(score) or score < 0 or score > 1:
            raise InputError(f"invalid score for {code} in {path}")
        result[str(code)] = score
    return result


def merged_scores(path: Path, as_of: str) -> dict[str, float]:
    document = load_json(path)
    entry = document.get(as_of)
    if not isinstance(entry, Mapping) or not isinstance(entry.get("adjacent15"), Mapping):
        raise InputError(f"merged score file has no scores for {as_of}: {path}")
    return {str(code): float(value) for code, value in entry["adjacent15"].items()}


def ndcg(rows: Sequence[Mapping[str, Any]], key: str, k: int) -> float:
    ordered = sorted(rows, key=lambda row: (-row[key], row["first_stage_rank"]))
    gains = [1.0 if row["actual"]["touch_1pct"] else 0.0 for row in ordered[:k]]
    dcg = sum(gain / math.log2(index + 2) for index, gain in enumerate(gains))
    ideal = [1.0] * min(k, sum(1 for row in rows if row["actual"]["touch_1pct"]))
    idcg = sum(gain / math.log2(index + 2) for index, gain in enumerate(ideal))
    return dcg / idcg if idcg else 0.0


def metrics(rows: Sequence[Mapping[str, Any]], key: str, universe_touches: int) -> dict[str, float]:
    ordered = sorted(rows, key=lambda row: (-row[key], row["first_stage_rank"]))

    def top(k: int) -> list[Mapping[str, Any]]:
        return list(ordered[:k])

    def hit(k: int) -> float:
        values = top(k)
        return sum(1 for row in values if row["actual"]["touch_1pct"]) / max(1, len(values))

    def recall(k: int) -> float:
        return sum(1 for row in top(k) if row["actual"]["touch_1pct"]) / max(1, universe_touches)

    def average(k: int, field: str) -> float:
        values = [float(row["actual"][field]) for row in top(k)]
        return sum(values) / len(values) if values else 0.0

    return {
        "recall@10": recall(10),
        "recall@30": recall(30),
        "recall@95": recall(95),
        "hit@10": hit(10),
        "hit@30": hit(30),
        "ndcg@10": ndcg(rows, key, 10),
        "ndcg@30": ndcg(rows, key, 30),
        "max_high@10": average(10, "max_high_return_pct"),
        "max_high@30": average(30, "max_high_return_pct"),
        "close3@10": average(10, "close3_return_pct"),
        "close3@30": average(30, "close3_return_pct"),
    }


def predict_tree(tree: Mapping[str, Any], values: Sequence[float]) -> float:
    index = 0
    nodes = tree["nodes"]
    while not nodes[index].get("leaf", False):
        node = nodes[index]
        value = values[int(node["feature"])]
        if math.isnan(value):
            index = int(node["left"] if node.get("missing_left", False) else node["right"])
        elif value <= float(node["threshold"]):
            index = int(node["left"])
        else:
            index = int(node["right"])
    return float(nodes[index].get("value", 0.0))


def predict_model(model: Mapping[str, Any], values: Sequence[float]) -> float:
    value = float(model["base"])
    for tree in model["trees"]:
        value += float(model["learning_rate"]) * predict_tree(tree, values)
    return max(float(model["label_clip_low"]), min(float(model["label_clip_high"]), value))


def tree_scores(
    stage1_candidates: Sequence[Mapping[str, Any]],
    stage2_candidates: Sequence[Mapping[str, Any]],
    jev_scores: Mapping[str, float],
    jev_top12: Mapping[str, float],
    model: Mapping[str, Any],
) -> dict[str, float]:
    base_pct = dict(zip(
        [str(row["code"]) for row in stage1_candidates],
        percentile([float(row["base_score"]) for row in stage1_candidates]),
    ))
    model_pct = dict(zip(
        [str(row["code"]) for row in stage1_candidates],
        percentile([float(row["model_score"]) for row in stage1_candidates]),
    ))
    pool_return5 = statistics.mean(float(row["features"]["return5"]) for row in stage2_candidates)
    groups = [list(stage2_candidates[start : start + TREE_GROUP_SIZE]) for start in range(0, len(stage2_candidates), TREE_GROUP_SIZE)]
    group_stats: dict[int, dict[str, Any]] = {}
    for group_index, group in enumerate(groups):
        values = [float(jev_scores[str(row["code"])]) for row in group]
        group_stats[group_index] = {
            "max": max(values) if values else 0.0,
            "pct": dict(zip([str(row["code"]) for row in group], percentile(values))),
            "top12_pct": {},
        }
        if group_index == 0:
            top12_values = [float(jev_top12.get(str(row["code"]), 0.0)) for row in group]
            group_stats[group_index]["top12_pct"] = dict(zip([str(row["code"]) for row in group], percentile(top12_values)))
    result: dict[str, float] = {}
    for index, candidate in enumerate(stage2_candidates):
        code = str(candidate["code"])
        group_index = index // TREE_GROUP_SIZE
        stats = group_stats[group_index]
        adjacent = float(jev_scores[code])
        adjacent_pct = float(stats["pct"].get(code, 0.0))
        top12 = float(jev_top12.get(code, 0.0)) if group_index == 0 else 0.0
        top12_pct = float(stats["top12_pct"].get(code, 0.0))
        features = candidate["features"]
        base_rank = float(candidate.get("base_rank", base_pct.get(code, 0.5)))
        model_rank = float(candidate.get("model_rank", model_pct.get(code, 0.5)))
        values = [
            float(candidate["blend_score"]),
            index / max(1, len(stage2_candidates) - 1),
            base_rank,
            model_rank,
            float(candidate["base_score"]),
            adjacent,
            adjacent_pct,
            adjacent / float(stats["max"]) if float(stats["max"]) > 0 else 0.0,
            top12,
            top12_pct,
            1.0,
            float(stats["max"]),
            float(features["return1"]),
            float(features["return5"]),
            float(features["return10"]),
            float(features["relative_return5"]),
            float(features["ma20_slope"]),
            float(features["ma20_distance"]),
            float(features["atr14_pct"]),
            float(features["rsi14"]),
            float(features["volume_ratio"]),
            float(features["close_position"]),
            float(features["upper_wick"]),
            float(features["high20_distance"]),
            float(features["low20_distance"]),
            float(features["touch_rate20"]),
            pool_return5,
        ]
        if int(model["feature_version"]) == 2:
            first_blend = max(0.0, min(1.0, values[0]))
            values.extend([
                adjacent_pct - (1.0 - values[1]),
                math.sqrt(first_blend * max(0.0, min(1.0, adjacent))),
                2.0 / (1.0 / max(first_blend, 1e-4) + 1.0 / max(adjacent_pct, 1e-4)),
                float(features["return5"]) / max(float(features["atr14_pct"]), 0.5),
            ])
        if len(values) != len(model["feature_names"]):
            raise InputError(f"tree feature count mismatch for {code}")
        result[code] = predict_model(model, values)
    return result


def rows_for_pool(
    candidates: Sequence[Mapping[str, Any]],
    labels_by_date: Mapping[str, Mapping[str, Mapping[str, Any]]],
    date: str,
    scores: Mapping[str, Mapping[str, float]],
) -> list[dict[str, Any]]:
    rows = []
    for index, candidate in enumerate(candidates, 1):
        code = str(candidate["code"])
        sample = labels_by_date[date].get(code)
        if sample is None:
            raise InputError(f"missing label for {date} {code}")
        row = dict(sample)
        row["first_stage_rank"] = index
        for ranker, values in scores.items():
            if code not in values:
                raise InputError(f"ranker {ranker} is missing {date} {code}")
            row[ranker] = float(values[code])
        rows.append(row)
    return rows


def build_scores(
    candidates: Sequence[Mapping[str, Any]],
    deepseek: Mapping[str, float],
    jev: Mapping[str, float] | None = None,
    tree: Mapping[str, float] | None = None,
) -> dict[str, dict[str, float]]:
    first = first_stage_percentiles(candidates)
    deepseek_pct = group_percentiles(candidates, deepseek)
    scores: dict[str, dict[str, float]] = {
        "baseline": {str(candidate["code"]): float(candidate["blend_score"]) for candidate in candidates},
        "deepseek": deepseek_pct,
        "deepseek_50": {code: 0.5 * first[code] + 0.5 * deepseek_pct[code] for code in first},
    }
    if jev is not None:
        jev_pct = group_percentiles(candidates, jev)
        scores["jev"] = jev_pct
        scores["deepseek_jev_40"] = {
            code: 0.2 * first[code] + 0.4 * deepseek_pct[code] + 0.4 * jev_pct[code]
            for code in first
        }
    if tree is not None:
        scores["tree"] = {str(candidate["code"]): float(tree[str(candidate["code"])]) for candidate in candidates}
        tree_pct = group_percentiles(candidates, tree)
        scores["deepseek_tree_40"] = {
            code: 0.2 * first[code] + 0.4 * deepseek_pct[code] + 0.4 * tree_pct[code]
            for code in first
        }
    return scores


def mean(values: Sequence[float]) -> float:
    return sum(values) / len(values) if values else 0.0


def aggregate(per_date: Mapping[str, Mapping[str, Mapping[str, float]]], dates: Sequence[str], rankers: Sequence[str]) -> dict[str, dict[str, float]]:
    output: dict[str, dict[str, float]] = {}
    for ranker in rankers:
        output[ranker] = {key: mean([float(per_date[date][ranker][key]) for date in dates]) for key in METRICS}
        output[ranker]["recall@95"] = mean([float(per_date[date][ranker]["recall@95"]) for date in dates])
    return output


def bootstrap_pair(per_date: Mapping[str, Mapping[str, Mapping[str, float]]], dates: Sequence[str], left: str, right: str, key: str, n: int = 2000, seed: int = 42) -> dict[str, float]:
    rng = random.Random(seed)
    deltas = [float(per_date[date][left][key]) - float(per_date[date][right][key]) for date in dates]
    samples = sorted(mean([rng.choice(deltas) for _ in deltas]) for _ in range(n))
    return {
        "mean": mean(deltas),
        "low": samples[int(0.025 * n)],
        "high": samples[int(0.975 * n) - 1],
        "wins": sum(1 for value in deltas if value > 1e-12),
        "losses": sum(1 for value in deltas if value < -1e-12),
        "ties": sum(1 for value in deltas if abs(value) <= 1e-12),
    }


def fmt_pct(value: float) -> str:
    return f"{value * 100:.1f}%"


def fmt_ret(value: float) -> str:
    return f"{value:+.2f}%"


def fmt_ndcg(value: float) -> str:
    return f"{value:.3f}"


def format_metric(key: str, value: float) -> str:
    if key.startswith("hit") or key.startswith("recall"):
        return fmt_pct(value)
    if key.startswith(("max_high", "close3")):
        return fmt_ret(value)
    return fmt_ndcg(value)


def markdown_table(
    title: str,
    aggregate_scores: Mapping[str, Mapping[str, float]],
    rankers: Sequence[str],
    columns: Sequence[tuple[str, str]],
) -> list[str]:
    lines = [f"### {title}", "", "| 排序器 | " + " | ".join(label for label, _ in columns) + " |", "|---|" + "---|" * len(columns)]
    for ranker in rankers:
        values = [format_metric(key, float(aggregate_scores[ranker][key])) for _, key in columns]
        lines.append(f"| {LABELS[ranker]} | " + " | ".join(values) + " |")
    return lines


def build_markdown(result: Mapping[str, Any], dates: Sequence[str]) -> str:
    stage2_rankers = ("baseline", "tree", "jev", "deepseek", "deepseek_50", "deepseek_jev_40", "deepseek_tree_40")
    lines = [
        "# DeepSeek 两阶段对比",
        "",
        f"- 测试日：{', '.join(dates)}（{len(dates)} 日）",
        "- 首层固定全池；第二层固定 first-stage Top95；所有模型使用 5 人组和 tie-aware 组内百分位。",
        "- DeepSeek 使用低延迟实验档 `reasoning_effort=none`、温度 0、三次选项顺序平均。",
        "- 树模型为 `second_stage_gbdt_v1`，JEV 使用同一冻结候选池的历史 artifact。",
        "- 标签是未来 3 个交易日最大最高价涨幅和收盘涨幅，不代表可成交收益或成功概率。",
        "",
    ]
    if "stage1" in result:
        lines.extend(markdown_table(
            "阶段一：全池初筛", result["stage1"]["aggregate"], ("baseline", "deepseek_stage1", "jev_stage1"),
            (("Recall@95", "recall@95"), ("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10")),
        ))
        lines.append("")
    lines.extend(markdown_table(
        "阶段二：Top95 精排", result["stage2"]["aggregate"], stage2_rankers,
        (("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10"), ("Hit@30", "hit@30"), ("MaxHigh@30", "max_high@30")),
    ))
    lines.append("")
    if "deepseek_pool" in result:
        lines.extend(markdown_table(
            "DeepSeek 自选 Top95 的端到端结果", result["deepseek_pool"]["aggregate"], ("baseline", "deepseek", "deepseek_two_stage"),
            (("Hit@10", "hit@10"), ("NDCG@10", "ndcg@10"), ("MaxHigh@10", "max_high@10"), ("Close3@10", "close3@10")),
        ))
        lines.append("")
    lines.append("### 测试期配对 bootstrap")
    lines.append("")
    lines.append("| 对比 | 指标 | 均值 | 95% CI | 胜/负/平 |")
    lines.append("|---|---|---:|---:|---:|")
    comparisons = (("deepseek", "tree"), ("deepseek", "jev"), ("deepseek", "baseline"), ("deepseek_jev_40", "jev"), ("deepseek_50", "baseline"))
    for left, right in comparisons:
        for key in ("hit@10", "ndcg@10", "max_high@10", "close3@10"):
            item = result["stage2"]["pairwise"][f"{left}_minus_{right}"][key]
            lines.append(
                f"| {LABELS[left]} − {LABELS[right]} | {key} | {format_metric(key, item['mean'])} | "
                f"[{format_metric(key, item['low'])}, {format_metric(key, item['high'])}] | {item['wins']}/{item['losses']}/{item['ties']} |"
            )
    return "\n".join(lines) + "\n"


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True, type=Path)
    parser.add_argument("--stage2-dataset", type=Path, help="labels for the local Top95 pool; defaults to --dataset")
    parser.add_argument("--stage1-candidates", type=Path)
    parser.add_argument("--stage2-candidates", required=True, type=Path)
    parser.add_argument("--deepseek-stage1-dir", type=Path)
    parser.add_argument("--deepseek-stage2-dir", required=True, type=Path)
    parser.add_argument("--deepseek-e2e-candidates", type=Path)
    parser.add_argument("--deepseek-e2e-stage2-dir", type=Path)
    parser.add_argument("--jev-stage1", type=Path)
    parser.add_argument("--jev-stage2", required=True, type=Path)
    parser.add_argument("--tree-model", type=Path, default=Path("internal/secondstage/model.json"))
    parser.add_argument("--test-dates", default="")
    parser.add_argument("--stage2-only", action="store_true")
    parser.add_argument("--out-dir", required=True, type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    dataset = load_json(args.dataset)
    labels_by_date: dict[str, dict[str, Mapping[str, Any]]] = defaultdict(dict)
    for sample in dataset["samples"]:
        labels_by_date[str(sample["as_of"])][str(sample["code"])] = sample
    stage2_dataset = load_json(args.stage2_dataset) if args.stage2_dataset else dataset
    stage2_labels: dict[str, dict[str, Mapping[str, Any]]] = defaultdict(dict)
    for sample in stage2_dataset["samples"]:
        stage2_labels[str(sample["as_of"])][str(sample["code"])] = sample
    dates = [value.strip() for value in args.test_dates.split(",") if value.strip()] if args.test_dates else list(DEFAULT_TEST_DATES)
    missing = [date for date in dates if date not in labels_by_date]
    if missing:
        raise InputError(f"dataset has no labels for {', '.join(missing)}")
    required = ["stage1_candidates", "deepseek_stage1_dir", "deepseek_e2e_candidates", "deepseek_e2e_stage2_dir", "jev_stage1"]
    if not args.stage2_only:
        missing_args = [name for name in required if getattr(args, name) is None]
        if missing_args:
            raise InputError(f"stage1 arguments are required unless --stage2-only: {', '.join(missing_args)}")
    stage1_doc = load_json(args.stage1_candidates) if not args.stage2_only else None
    stage2_doc = load_json(args.stage2_candidates)
    e2e_doc = load_json(args.deepseek_e2e_candidates) if not args.stage2_only else None
    tree_model = load_json(args.tree_model)
    stage1_per_date: dict[str, dict[str, dict[str, float]]] = {}
    stage2_per_date: dict[str, dict[str, dict[str, float]]] = {}
    e2e_per_date: dict[str, dict[str, dict[str, float]]] = {}
    universe: dict[str, dict[str, int]] = {}
    for date in dates:
        stage1_candidates = sorted(stage1_doc["dates"][date], key=lambda row: int(row["rank"])) if stage1_doc is not None else []
        stage2_candidates = sorted(stage2_doc["dates"][date], key=lambda row: int(row["rank"]))
        e2e_candidates = sorted(e2e_doc["dates"][date], key=lambda row: int(row["rank"])) if e2e_doc is not None else []
        deepseek2 = score_artifact(args.deepseek_stage2_dir / f"{date}.json", date)
        jev_entry = load_json(args.jev_stage2)[date]
        jev2 = {str(code): float(value) for code, value in jev_entry["adjacent15"].items()}
        jev_top12 = {str(code): float(value) for code, value in jev_entry.get("full15_top12", {}).items()}
        tree = tree_scores(stage1_candidates, stage2_candidates, jev2, jev_top12, tree_model)

        stage1_touches = sum(1 for sample in labels_by_date[date].values() if sample["actual"]["touch_1pct"])
        if not args.stage2_only:
            deepseek1 = score_artifact(args.deepseek_stage1_dir / f"{date}.json", date)
            deepseek_e2e = score_artifact(args.deepseek_e2e_stage2_dir / f"{date}.json", date)
            jev1 = merged_scores(args.jev_stage1, date)
            stage1_scores = {
                "baseline": {str(row["code"]): float(row["blend_score"]) for row in stage1_candidates},
                "deepseek_stage1": group_percentiles(stage1_candidates, deepseek1),
                "jev_stage1": group_percentiles(stage1_candidates, jev1),
            }
            stage1_rows = rows_for_pool(stage1_candidates, labels_by_date, date, stage1_scores)
            stage1_per_date[date] = {ranker: metrics(stage1_rows, ranker, stage1_touches) for ranker in stage1_scores}

        stage2_scores = build_scores(stage2_candidates, deepseek2, jev=jev2, tree=tree)
        stage2_rows = rows_for_pool(stage2_candidates, stage2_labels, date, stage2_scores)
        stage2_touches = sum(1 for sample in stage2_labels[date].values() if sample["actual"]["touch_1pct"])
        stage2_per_date[date] = {ranker: metrics(stage2_rows, ranker, stage2_touches) for ranker in stage2_scores}

        if not args.stage2_only:
            e2e_scores = build_scores(e2e_candidates, deepseek_e2e)
            e2e_first = first_stage_percentiles(e2e_candidates)
            e2e_stage1_pct = group_percentiles(e2e_candidates, deepseek1)
            e2e_stage2_pct = group_percentiles(e2e_candidates, deepseek_e2e)
            e2e_scores["deepseek_two_stage"] = {code: 0.2 * e2e_stage1_pct[code] + 0.8 * e2e_stage2_pct[code] for code in e2e_first}
            e2e_rows = rows_for_pool(e2e_candidates, labels_by_date, date, e2e_scores)
            e2e_per_date[date] = {ranker: metrics(e2e_rows, ranker, stage1_touches) for ranker in e2e_scores}
        universe[date] = {
            "universe": len(labels_by_date[date]),
            "stage1_pool": len(stage1_candidates),
            "stage2_pool": len(stage2_candidates),
            "deepseek_pool": len(e2e_candidates),
        }

    stage2_rankers = ("baseline", "tree", "jev", "deepseek", "deepseek_50", "deepseek_jev_40", "deepseek_tree_40")
    stage2_aggregate = aggregate(stage2_per_date, dates, stage2_rankers)
    pairwise = {
        f"{left}_minus_{right}": {
            key: bootstrap_pair(stage2_per_date, dates, left, right, key)
            for key in ("hit@10", "ndcg@10", "max_high@10", "close3@10")
        }
        for left, right in (("deepseek", "tree"), ("deepseek", "jev"), ("deepseek", "baseline"), ("deepseek_jev_40", "jev"), ("deepseek_50", "baseline"))
    }
    result: dict[str, Any] = {
        "protocol": {
            "dates": dates,
            "group_size": GROUP_SIZE,
            "tree_group_size": TREE_GROUP_SIZE,
            "deepseek_reasoning_effort": "none",
            "deepseek_permutations": 3,
        },
        "universe": universe,
        "stage2": {
            "aggregate": stage2_aggregate,
            "per_date": stage2_per_date,
            "pairwise": pairwise,
        },
    }
    if not args.stage2_only:
        result["stage1"] = {
            "aggregate": aggregate(stage1_per_date, dates, ("baseline", "deepseek_stage1", "jev_stage1")),
            "per_date": stage1_per_date,
        }
        result["deepseek_pool"] = {
            "aggregate": aggregate(e2e_per_date, dates, ("baseline", "deepseek", "deepseek_two_stage")),
            "per_date": e2e_per_date,
        }
    args.out_dir.mkdir(parents=True, exist_ok=True)
    write_json_atomic(args.out_dir / "comparison.json", result)
    markdown = build_markdown(result, dates)
    (args.out_dir / "comparison.md").write_text(markdown, encoding="utf-8")
    print(markdown)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
