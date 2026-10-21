#!/usr/bin/env python3
"""Score first-stage candidates with a local Laya typed-decision model.

The output is the same versioned artifact consumed by internal/secondstage:
candidates are scored in deterministic groups (fifteen by default; smaller
groups are supported for long-context or CPU-bound research), three option
orders are averaged, and only the resulting probabilities are written. The production
Go process never imports Laya or starts a Python subprocess.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import tempfile
import time
from collections import defaultdict
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence


SCHEMA_VERSION = 1
PROMPT_VERSION = "second-stage-adjacent15-v1"
GROUP_SIZE = 15
TOP_K_MASS = 12
CHECKPOINT_MODELS = {
    "official": "convaiinnovations/laya",
    "multilingual": "convaiinnovations/laya-multilingual",
}
QUESTION = (
    "Which candidate has the strongest 3-day continuation setup with acceptable "
    "reversal risk? Choose one candidate."
)


class InputError(ValueError):
    """Raised when an input document or model response violates the contract."""


def rounded(value: Any, digits: int = 1) -> float:
    try:
        number = float(value)
    except (TypeError, ValueError) as exc:
        raise InputError(f"feature value {value!r} is not numeric") from exc
    if not math.isfinite(number):
        raise InputError(f"feature value {value!r} is not finite")
    return round(number, digits)


def feature_value(features: Mapping[str, Any], *names: str) -> Any:
    for name in names:
        if name in features:
            return features[name]
    raise InputError(f"missing feature: {names[0]}")


def short_candidate(candidate: Mapping[str, Any], features: Mapping[str, Any], candidate_id: str) -> dict[str, Any]:
    return {
        "id": candidate_id,
        "close_vs_ma20_pct": rounded(feature_value(features, "ma20_distance")),
        "ma20_slope_5d_pct": rounded(feature_value(features, "ma20_slope")),
        "return_5d_pct": rounded(feature_value(features, "return5")),
        "relative_strength_5d_pct": rounded(feature_value(features, "rel_return5", "relative5")),
        "rsi14": rounded(feature_value(features, "rsi14"), 0),
        "close_position": rounded(feature_value(features, "close_position"), 2),
        "volume_ratio": rounded(feature_value(features, "volume_ratio5", "volume_ratio"), 2),
        "distance_to_20d_high_pct": rounded(feature_value(features, "high20_distance")),
        "atr14_pct": rounded(feature_value(features, "atr14_pct", "atr14")),
        "upper_wick": rounded(feature_value(features, "upper_wick"), 2),
    }


def group_request(candidates: Sequence[Mapping[str, Any]], as_of: str, group_index: int) -> tuple[dict[str, Any], dict[str, str], list[list[str]]]:
    if len(candidates) < 2:
        raise InputError("a scoring group needs at least two candidates")
    rows = [
        short_candidate(candidate, candidate["_features"], f"C{index + 1}")
        for index, candidate in enumerate(candidates)
    ]
    state = {"as_of": as_of, "candidates": rows}
    options = [
        {"id": row["id"], "description": f"Candidate {row['id']}; facts are in candidates."}
        for row in rows
    ]
    permutations = [options, list(reversed(options)), options[1:] + options[:1]]
    orders = [[option["id"] for option in permutation] for permutation in permutations]
    code_by_id = {f"C{index + 1}": candidate["code"] for index, candidate in enumerate(candidates)}
    return state, code_by_id, orders


def questions_for_orders(orders: Sequence[Sequence[str]], code_by_id: Mapping[str, str]) -> dict[str, Any]:
    questions: dict[str, Any] = {}
    for index, order in enumerate(orders):
        if set(order) != set(code_by_id):
            raise InputError("option order does not match the candidate group")
        questions[f"rank{index}"] = {
            "type": "choice",
            "instructions": QUESTION,
            "criteria": {
                option_id: f"Candidate {option_id}; facts are in candidates."
                for option_id in order
            },
        }
    return questions


def question_for_order(state: Mapping[str, Any], order: Sequence[str], code_by_id: Mapping[str, str]) -> dict[str, Any]:
    return {"rank": questions_for_orders([order], code_by_id)["rank0"]}


def _probabilities(response: Any, code_by_id: Mapping[str, str], question_id: str = "rank") -> dict[str, float]:
    try:
        probabilities = response["answers"][question_id]["probabilities"]
    except (KeyError, TypeError) as exc:
        raise InputError(f"Laya response is missing answers.{question_id}.probabilities") from exc
    if not isinstance(probabilities, Mapping):
        raise InputError("Laya probabilities must be an object")
    if set(probabilities) != set(code_by_id):
        raise InputError("Laya probabilities do not match the candidate group")
    result: dict[str, float] = {}
    total = 0.0
    for option_id, value in probabilities.items():
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise InputError(f"Laya probability for {option_id!r} is not numeric")
        probability = float(value)
        if not math.isfinite(probability) or probability < 0.0 or probability > 1.0:
            raise InputError(f"Laya probability for {option_id!r} is out of range")
        result[option_id] = probability
        total += probability
    if total <= 0.0:
        raise InputError("Laya probabilities sum to zero")
    return result


def top_k_mass(scores: Mapping[str, float], ordered_codes: Sequence[str], limit: int) -> dict[str, float]:
    subset = list(ordered_codes[:limit])
    total = sum(scores.get(code, 0.0) for code in subset)
    if total <= 0.0:
        raise InputError("top-k probability mass is zero")
    result = {code: 0.0 for code in ordered_codes}
    for code in subset:
        result[code] = scores.get(code, 0.0) / total
    return result


def _check_usage(response: Mapping[str, Any]) -> None:
    usage = response.get("usage")
    if isinstance(usage, Mapping) and usage.get("truncated"):
        raise InputError(
            "Laya context was truncated: increase --max-len or reduce the group size"
        )


def score_candidates(
    predict: Callable[[Mapping[str, Any], Mapping[str, Any]], Any],
    candidates: Sequence[Mapping[str, Any]],
    as_of: str,
    max_groups: int = 0,
    batch: Callable[[Sequence[Mapping[str, Any]], Mapping[str, Any]], Sequence[Any]] | None = None,
    group_size: int = GROUP_SIZE,
) -> tuple[dict[str, float], dict[str, float]]:
    if len(candidates) < 2:
        raise InputError("at least two candidates are required")
    codes = [candidate.get("code", "") for candidate in candidates]
    if any(not isinstance(code, str) or not code.strip() for code in codes):
        raise InputError("every candidate needs a code")
    if len(set(codes)) != len(codes):
        raise InputError("candidate codes must be unique")

    if group_size < 2:
        raise InputError("group size must be at least two")
    prepared = []
    for start in range(0, len(candidates), group_size):
        if max_groups and len(prepared) >= max_groups:
            break
        group = candidates[start:start + group_size]
        if len(group) < 2:
            continue
        state, code_by_id, orders = group_request(group, as_of, len(prepared))
        prepared.append((state, code_by_id, orders))
    if not prepared:
        raise InputError("no scorable group was selected")

    by_code: dict[str, list[float]] = defaultdict(list)
    if batch is not None:
        # The final group may be shorter; option IDs C1..Cn must not refer to
        # candidates that are absent from that state.  Bucket by group size so
        # each forward pass uses a question set that matches its states.
        buckets: dict[int, list[tuple[Mapping[str, Any], Mapping[str, str], Sequence[Sequence[str]]]]] = defaultdict(list)
        for item in prepared:
            buckets[len(item[1])].append(item)
        for size in sorted(buckets):
            items = buckets[size]
            questions = questions_for_orders(items[0][2], items[0][1])
            responses = batch([item[0] for item in items], questions)
            if len(responses) != len(items):
                raise InputError("Laya batch returned the wrong number of responses")
            for response, (_, code_by_id, _) in zip(responses, items):
                _check_usage(response)
                for question_id in questions:
                    probabilities = _probabilities(response, code_by_id, question_id)
                    for option_id, probability in probabilities.items():
                        by_code[code_by_id[option_id]].append(probability)
    else:
        for state, code_by_id, orders in prepared:
            for order in orders:
                response = predict(state, question_for_order(state, order, code_by_id))
                _check_usage(response)
                probabilities = _probabilities(response, code_by_id)
                for option_id, probability in probabilities.items():
                    by_code[code_by_id[option_id]].append(probability)

    missing = [code for code in codes if code not in by_code]
    if missing:
        raise InputError(f"Laya did not score every candidate: {', '.join(missing[:5])}")
    adjacent = {
        code: sum(by_code[code]) / len(by_code[code])
        for code in codes
    }
    first_codes = list(codes[:group_size])
    top12 = top_k_mass(adjacent, first_codes, min(TOP_K_MASS, group_size))
    return adjacent, top12


def build_artifact(
    as_of: str,
    ordered_codes: Sequence[str],
    adjacent: Mapping[str, float],
    top12: Mapping[str, float],
    model: Mapping[str, Any],
    prompt_version: str = PROMPT_VERSION,
) -> dict[str, Any]:
    scores: dict[str, dict[str, float]] = {}
    for code in ordered_codes:
        candidate_score = {
            "adjacent15": float(adjacent[code]),
            "full15_top12": float(top12.get(code, 0.0)),
        }
        for value in candidate_score.values():
            if not math.isfinite(value) or value < 0.0 or value > 1.0:
                raise InputError(f"invalid Laya score for {code}")
        scores[code] = candidate_score
    return {
        "schema_version": SCHEMA_VERSION,
        "as_of": as_of,
        "scores": scores,
        "prompt_version": prompt_version,
        "model": dict(model),
    }


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


def load_inputs(candidates_path: Path, dataset_path: Path) -> tuple[dict[str, list[dict[str, Any]]], dict[tuple[str, str], dict[str, Any]]]:
    try:
        candidates_doc = json.loads(candidates_path.read_text(encoding="utf-8"))
        dataset = json.loads(dataset_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise InputError(f"cannot read candidate or feature input: {exc}") from exc
    if candidates_doc.get("schema_version") != SCHEMA_VERSION:
        raise InputError("candidate schema_version must be 1")
    dates = candidates_doc.get("dates")
    if not isinstance(dates, dict) or not dates:
        raise InputError("candidate dates must be a non-empty object")
    samples = dataset.get("samples")
    if not isinstance(samples, list):
        raise InputError("dataset samples must be a list")
    features: dict[tuple[str, str], dict[str, Any]] = {}
    for sample in samples:
        if not isinstance(sample, dict):
            raise InputError("dataset sample must be an object")
        key = (sample.get("as_of"), sample.get("code"))
        if not all(isinstance(value, str) and value for value in key):
            raise InputError("dataset sample needs as_of and code")
        if key in features:
            raise InputError(f"duplicate dataset sample: {key[0]} {key[1]}")
        sample_features = sample.get("features")
        if not isinstance(sample_features, dict):
            raise InputError(f"dataset sample has no features: {key[0]} {key[1]}")
        features[key] = sample_features
    return {date: list(rows) for date, rows in dates.items()}, features


def candidates_for_date(
    as_of: str,
    rows: Sequence[Any],
    features: Mapping[tuple[str, str], Mapping[str, Any]],
) -> list[dict[str, Any]]:
    if not isinstance(rows, list):
        raise InputError(f"candidates for {as_of} must be a list")
    candidates: list[dict[str, Any]] = []
    seen: set[str] = set()
    for row in rows:
        if not isinstance(row, dict) or not isinstance(row.get("code"), str):
            raise InputError(f"candidate for {as_of} needs a string code")
        code = row["code"]
        if code in seen:
            raise InputError(f"duplicate candidate for {as_of}: {code}")
        seen.add(code)
        sample_features = features.get((as_of, code))
        if sample_features is None:
            raise InputError(f"missing features for {as_of} {code}")
        candidates.append({**row, "_features": sample_features})
    return candidates


def laya_loader(args: argparse.Namespace) -> tuple[Callable[[Mapping[str, Any], Mapping[str, Any]], Any], dict[str, Any]]:
    try:
        import laya
    except ImportError as exc:
        raise InputError("Laya is not installed; run `python3 -m pip install laya`") from exc
    try:
        import torch
        torch.set_num_threads(int(os.environ.get("LAYA_THREADS", "16")))
        agent = laya.load(
            args.model or CHECKPOINT_MODELS[args.checkpoint],
            device=args.device,
            revision=args.revision,
            token=os.environ.get("HF_TOKEN"),
        )
    except Exception as exc:
        raise InputError(f"cannot load Laya model {args.model!r}: {exc}") from exc

    def predict(state: Mapping[str, Any], questions: Mapping[str, Any]) -> Any:
        return agent.system_one(
            state,
            questions,
            max_len=args.max_len or None,
            head_max_len=args.head_max_len or None,
        )

    def predict_batch(states: Sequence[Mapping[str, Any]], questions: Mapping[str, Any]) -> Any:
        return agent.predict_batch(
            list(states),
            questions,
            batch_size=args.batch_size,
            max_len=args.max_len or None,
            head_max_len=args.head_max_len or None,
            sort_by_length=True,
        )

    predict.batch = predict_batch  # type: ignore[attr-defined]

    metadata = {
        "provider": "laya",
        "group_size": args.group_size,
        "family": "Laya System 1 typed decisions",
        "checkpoint": args.checkpoint,
        "checkpoint_repo": CHECKPOINT_MODELS[args.checkpoint],
        "model": args.model or CHECKPOINT_MODELS[args.checkpoint],
        "package_version": getattr(laya, "__version__", ""),
        "device": str(getattr(agent, "device", "cpu")),
        "revision": args.revision or "default",
    }
    return predict, metadata


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidates", required=True, type=Path)
    parser.add_argument("--dataset", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--dates", default="", help="comma-separated as-of dates")
    parser.add_argument("--max-dates", type=int, default=0)
    parser.add_argument("--max-groups", type=int, default=0)
    parser.add_argument("--batch-size", type=int, default=4)
    parser.add_argument("--group-size", type=int, default=GROUP_SIZE)
    parser.add_argument("--checkpoint", choices=sorted(CHECKPOINT_MODELS), default="official")
    parser.add_argument("--model", default=None, help="override the checkpoint repository or local path")
    parser.add_argument("--device", default=None)
    parser.add_argument("--revision", default=None)
    parser.add_argument("--max-len", type=int, default=0)
    parser.add_argument("--head-max-len", type=int, default=0)
    parser.add_argument("--skip-existing", action="store_true")
    return parser.parse_args(argv)


def main(
    argv: Sequence[str] | None = None,
    loader: Callable[[argparse.Namespace], tuple[Callable[[Mapping[str, Any], Mapping[str, Any]], Any], dict[str, Any]]] = laya_loader,
) -> int:
    args = parse_args(argv)
    candidates_by_date, features = load_inputs(args.candidates, args.dataset)
    dates = sorted(candidates_by_date)
    if args.dates:
        wanted = {value.strip() for value in args.dates.split(",") if value.strip()}
        unknown = wanted - set(dates)
        if unknown:
            raise InputError(f"unknown candidate date: {', '.join(sorted(unknown))}")
        dates = [date for date in dates if date in wanted]
    if args.max_dates:
        dates = dates[:args.max_dates]
    if not dates:
        raise InputError("no candidate dates selected")

    pending = [date for date in dates if not (args.skip_existing and (args.output_dir / f"{date}.json").exists())]
    if not pending:
        print("all selected dates already have artifacts", flush=True)
        return 0

    prepared = [
        (as_of, candidates_for_date(as_of, candidates_by_date[as_of], features))
        for as_of in pending
    ]
    predict, metadata = loader(args)
    started = time.perf_counter()
    for index, (as_of, candidates) in enumerate(prepared, 1):
        adjacent, top12 = score_candidates(
            predict,
            candidates,
            as_of,
            max_groups=args.max_groups,
            batch=getattr(predict, "batch", None),
            group_size=args.group_size,
        )
        prompt_version = PROMPT_VERSION if args.group_size == GROUP_SIZE else f"second-stage-adjacent{args.group_size}-v1"
        artifact = build_artifact(as_of, [candidate["code"] for candidate in candidates], adjacent, top12, metadata, prompt_version)
        output = args.output_dir / f"{as_of}.json"
        write_json_atomic(output, artifact)
        print(
            f"{index}/{len(pending)} {as_of} candidates={len(candidates)} "
            f"scored={len(artifact['scores'])} elapsed={time.perf_counter() - started:.1f}s -> {output}",
            flush=True,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
