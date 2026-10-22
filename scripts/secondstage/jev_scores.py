#!/usr/bin/env python3
"""Score second-stage candidates with the local JEV-CPU Qwen3 model.

This is the offline artifact producer used by the daily post-close pipeline.
It mirrors the validated adjacent-group option-logit protocol and never runs
inside the production Go process.
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
from typing import Any, Mapping, Sequence


SCHEMA_VERSION = 1
GROUP_SIZE = 15
TOP_K_MASS = 12
QUESTION = (
    "Which candidate has the strongest 3-day continuation setup with acceptable "
    "reversal risk? Choose one candidate."
)
SYSTEM_PROMPT = (
    "Apply the supplied criterion to the supplied evidence. Choose exactly one listed option. "
    "Respond with only its uppercase letter, with no explanation or reasoning."
)
LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
DEFAULT_MODEL = Path("/mnt/windows_database/download_models/jev/Qwen3-0.6B")


class InputError(ValueError):
    """Raised when an input or model response violates the artifact contract."""


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


def short_candidate(features: Mapping[str, Any], candidate_id: str) -> dict[str, Any]:
    return {
        "id": candidate_id,
        "close_vs_ma20_pct": rounded(feature_value(features, "ma20_distance")),
        "ma20_slope_5d_pct": rounded(feature_value(features, "ma20_slope")),
        "return_5d_pct": rounded(feature_value(features, "return5")),
        "relative_strength_5d_pct": rounded(feature_value(features, "rel_return5", "relative_return5")),
        "rsi14": rounded(feature_value(features, "rsi14"), 0),
        "close_position": rounded(feature_value(features, "close_position"), 2),
        "volume_ratio": rounded(feature_value(features, "volume_ratio5", "volume_ratio"), 2),
        "distance_to_20d_high_pct": rounded(feature_value(features, "high20_distance")),
        "atr14_pct": rounded(feature_value(features, "atr14_pct", "atr14")),
        "upper_wick": rounded(feature_value(features, "upper_wick"), 2),
    }


def prepare_groups(candidates: Sequence[Mapping[str, Any]], features: Mapping[tuple[str, str], Mapping[str, Any]], as_of: str, group_size: int) -> list[tuple[list[str], list[dict[str, Any]]]]:
    groups: list[tuple[list[str], list[dict[str, Any]]]] = []
    for start in range(0, len(candidates), group_size):
        group = candidates[start:start + group_size]
        if len(group) < 2:
            continue
        codes = [str(item["code"]) for item in group]
        rows = []
        for index, code in enumerate(codes):
            item_features = features.get((as_of, code))
            if item_features is None:
                raise InputError(f"missing features for {as_of} {code}")
            rows.append(short_candidate(item_features, f"C{index + 1}"))
        groups.append((codes, rows))
    if not groups:
        raise InputError("no scorable candidate group was selected")
    return groups


def row_for_group(as_of: str, group: Sequence[dict[str, Any]], permutation: Sequence[int]) -> dict[str, Any]:
    state = {"as_of": as_of, "candidates": list(group)}
    options = [
        {"id": row["id"], "description": f"Candidate {row['id']}; facts are in candidates."}
        for row in group
    ]
    return {
        "id": f"{as_of}|adjacent{len(group)}|perm{''.join(str(i) for i in permutation)}",
        "state": state,
        "question": QUESTION,
        "options": [options[index] for index in permutation],
    }


def direct_prompt(tokenizer: Any, row: Mapping[str, Any]) -> str:
    payload = {
        "evidence": row["state"],
        "criterion": row["question"],
        "options": [
            {"letter": LETTERS[index], "description": option["description"]}
            for index, option in enumerate(row["options"])
        ],
    }
    return tokenizer.apply_chat_template(
        [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": json.dumps(payload, ensure_ascii=False)},
        ],
        tokenize=False,
        add_generation_prompt=True,
        enable_thinking=False,
    )


def softmax(values: Sequence[float]) -> list[float]:
    if len(values) < 2 or any(not math.isfinite(value) for value in values):
        raise InputError("model returned invalid option logits")
    maximum = max(values)
    weights = [math.exp(value - maximum) for value in values]
    total = sum(weights)
    if total <= 0 or not math.isfinite(total):
        raise InputError("model option logits cannot be normalized")
    return [weight / total for weight in weights]


def score_row(model: Any, tokenizer: Any, row: Mapping[str, Any], max_tokens: int) -> dict[str, float]:
    import torch

    prompt = direct_prompt(tokenizer, row)
    ids = tokenizer.encode(prompt, add_special_tokens=False)
    if not ids or len(ids) > max_tokens:
        raise InputError(f"JEV input is {len(ids)} tokens; the limit is {max_tokens}")
    slots = []
    for letter in LETTERS[: len(row["options"])]:
        encoded = tokenizer.encode(letter, add_special_tokens=False)
        if len(encoded) != 1:
            raise InputError(f"option letter {letter!r} is not a single token")
        slots.append(encoded[0])
    device = next(model.parameters()).device
    inputs = {
        "input_ids": torch.tensor([ids], dtype=torch.long, device=device),
        "attention_mask": torch.ones((1, len(ids)), dtype=torch.long, device=device),
    }
    with torch.inference_mode():
        output = model(**inputs, use_cache=False, return_dict=True, logits_to_keep=1)
    selected = output.logits[0, -1, torch.tensor(slots, dtype=torch.long, device=device)].float().cpu().tolist()
    probabilities = softmax(selected)
    return {str(option["id"]): probability for option, probability in zip(row["options"], probabilities)}


def score_group(model: Any, tokenizer: Any, as_of: str, codes: Sequence[str], rows: Sequence[dict[str, Any]], max_tokens: int) -> dict[str, float]:
    by_code: dict[str, list[float]] = defaultdict(list)
    for permutation in (list(range(len(rows))), list(reversed(range(len(rows)))), list(range(1, len(rows))) + [0]):
        row = row_for_group(as_of, rows, permutation)
        probabilities = score_row(model, tokenizer, row, max_tokens)
        for index, code in enumerate(codes):
            by_code[code].append(probabilities[f"C{index + 1}"])
    return {code: sum(by_code[code]) / len(by_code[code]) for code in codes}


def load_model(model_path: Path, threads: int) -> tuple[Any, Any, dict[str, Any]]:
    try:
        import torch
        import transformers
        from transformers import AutoModelForCausalLM, AutoTokenizer
    except ImportError as exc:
        raise InputError("JEV inference requires torch and transformers") from exc
    if threads < 1:
        raise InputError("--threads must be positive")
    if not (model_path / "model.safetensors").is_file():
        raise InputError(f"missing JEV model weights: {model_path}")
    torch.set_num_threads(threads)
    tokenizer = AutoTokenizer.from_pretrained(str(model_path), local_files_only=True)
    model = AutoModelForCausalLM.from_pretrained(
        str(model_path),
        dtype=torch.float32,
        low_cpu_mem_usage=True,
        local_files_only=True,
    ).eval()
    metadata = {
        "family": "Qwen3-0.6B + JEV-CPU option-logit readout",
        "provider": "jev-cpu",
        "model": str(model_path),
        "dtype": "float32",
        "device": "cpu",
        "threads": threads,
        "torch_version": getattr(torch, "__version__", ""),
        "transformers_version": getattr(transformers, "__version__", ""),
    }
    return model, tokenizer, metadata


def top_k_mass(scores: Mapping[str, float], codes: Sequence[str], limit: int) -> dict[str, float]:
    subset = list(codes[:limit])
    total = sum(scores.get(code, 0.0) for code in subset)
    if total <= 0:
        return {code: 0.0 for code in codes}
    return {code: (scores.get(code, 0.0) / total if code in subset else 0.0) for code in codes}


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


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidates", required=True, type=Path)
    parser.add_argument("--dataset", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--as-of", required=True)
    parser.add_argument("--group-size", type=int, default=GROUP_SIZE)
    parser.add_argument("--model", type=Path, default=Path(os.environ.get("JEV_MODEL_DIR", DEFAULT_MODEL)))
    parser.add_argument("--threads", type=int, default=int(os.environ.get("JEV_THREADS", "8")))
    parser.add_argument("--max-len", type=int, default=4096)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    if args.group_size < 2:
        raise InputError("--group-size must be at least 2")
    candidates_doc = json.loads(args.candidates.read_text(encoding="utf-8"))
    dataset = json.loads(args.dataset.read_text(encoding="utf-8"))
    candidates = candidates_doc.get("dates", {}).get(args.as_of)
    if not isinstance(candidates, list) or not candidates:
        raise InputError(f"candidates do not contain {args.as_of}")
    features = {
        (sample.get("as_of"), sample.get("code")): sample.get("features")
        for sample in dataset.get("samples", [])
        if isinstance(sample, dict)
    }
    groups = prepare_groups(candidates, features, args.as_of, args.group_size)
    model, tokenizer, metadata = load_model(args.model, args.threads)
    started = time.perf_counter()
    adjacent: dict[str, float] = {}
    for codes, group in groups:
        adjacent.update(score_group(model, tokenizer, args.as_of, codes, group, args.max_len))
    ordered = [str(item["code"]) for item in candidates]
    missing = [code for code in ordered if code not in adjacent]
    if missing:
        raise InputError(f"JEV did not score every candidate: {', '.join(missing[:5])}")
    head_count = min(15, len(ordered))
    head_codes = ordered[:head_count]
    head_rows = prepare_groups(candidates[:head_count], features, args.as_of, head_count)[0][1]
    head_scores = score_group(model, tokenizer, args.as_of, head_codes, head_rows, args.max_len)
    top12 = top_k_mass(head_scores, head_codes, min(TOP_K_MASS, head_count))
    artifact = {
        "schema_version": SCHEMA_VERSION,
        "as_of": args.as_of,
        "scores": {
            code: {"adjacent15": adjacent[code], "full15_top12": top12.get(code, 0.0)}
            for code in ordered
        },
        "prompt_version": f"second-stage-adjacent{args.group_size}-v1",
        "model": {**metadata, "group_size": args.group_size},
    }
    output = args.output_dir / f"{args.as_of}.json"
    write_json_atomic(output, artifact)
    print(f"wrote {output}: {len(ordered)} scores, groups={len(groups)}, elapsed={time.perf_counter() - started:.1f}s", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
