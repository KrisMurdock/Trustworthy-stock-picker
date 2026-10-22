#!/usr/bin/env python3
"""Score second-stage candidates with Chinese Decision 0.8B.

The adapter follows the model's official single-forward choice protocol and
reuses the Laya scorer's grouping, option-order averaging, and artifact schema.
It only produces offline artifacts; the production Go process never loads
PyTorch or invokes this script.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import sys
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping, Sequence

SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

import laya_scores


DEFAULT_MODEL = "quincyqiang/chinese-decision-0.8b"
LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"


def text(value: Any) -> str:
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def options_for(question: Mapping[str, Any]) -> list[tuple[str, str]]:
    kind = question.get("type")
    criteria = question.get("criteria")
    if kind == "choice":
        if not isinstance(criteria, Mapping) or not criteria:
            raise laya_scores.InputError("choice question needs non-empty criteria")
        options = [(str(key), text(value)) for key, value in criteria.items()]
    elif kind == "score":
        if not isinstance(criteria, Sequence) or isinstance(criteria, (str, bytes)) or not criteria:
            raise laya_scores.InputError("score question needs a non-empty criteria list")
        options = [(str(index), text(value)) for index, value in enumerate(criteria)]
    elif kind == "noul":
        criteria = criteria if isinstance(criteria, Mapping) else {}
        options = [("false", text(criteria.get("false", "否"))), ("true", text(criteria.get("true", "是")))]
    else:
        raise laya_scores.InputError(f"unsupported Chinese Decision question type: {kind!r}")
    if len(options) > len(LETTERS):
        raise laya_scores.InputError("Chinese Decision supports at most 26 options")
    return options


def compile_prompt(
    tokenizer: Any,
    state: Mapping[str, Any],
    instructions: Any,
    options: Sequence[tuple[str, str]],
) -> str:
    content = (
        "待判断的上下文：\n"
        + text(state)
        + "\n\n问题：\n"
        + text(instructions)
        + "\n\n候选项：\n"
        + "\n".join(f"{LETTERS[index]}: {key} — {value}" for index, (key, value) in enumerate(options))
        + "\n只返回候选项对应的字母。"
    )
    return tokenizer.apply_chat_template(
        [{"role": "user", "content": content}],
        tokenize=False,
        add_generation_prompt=True,
        enable_thinking=False,
    )


def calibrated_probabilities(logits: Iterable[float], temperature: float) -> list[float]:
    if not math.isfinite(temperature) or temperature <= 0.0:
        raise laya_scores.InputError("Chinese Decision temperature must be positive")
    values = [float(value) / temperature for value in logits]
    if not values or any(not math.isfinite(value) for value in values):
        raise laya_scores.InputError("Chinese Decision returned invalid logits")
    maximum = max(values)
    exponentials = [math.exp(value - maximum) for value in values]
    total = sum(exponentials)
    if total <= 0.0 or not math.isfinite(total):
        raise laya_scores.InputError("Chinese Decision probabilities cannot be normalized")
    return [value / total for value in exponentials]


def decision_config(model: str, revision: str | None) -> dict[str, Any]:
    local = Path(model) / "decision_config.json"
    if local.exists():
        path = local
    else:
        try:
            from huggingface_hub import hf_hub_download
        except ImportError as exc:
            raise laya_scores.InputError("huggingface_hub is required to read the remote decision config") from exc
        path = Path(hf_hub_download(repo_id=model, filename="decision_config.json", revision=revision))
    try:
        config = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise laya_scores.InputError(f"cannot read Chinese Decision config: {exc}") from exc
    if not isinstance(config, dict):
        raise laya_scores.InputError("Chinese Decision config must be an object")
    return config


def decision_loader(
    args: argparse.Namespace,
) -> tuple[Callable[[Mapping[str, Any], Mapping[str, Any]], Any], dict[str, Any]]:
    try:
        import torch
        import transformers
        from transformers import AutoModelForCausalLM, AutoTokenizer
    except ImportError as exc:
        raise laya_scores.InputError(
            "Chinese Decision inference requires torch>=2.8 and transformers>=5.3"
        ) from exc

    threads = int(args.threads or os.environ.get("CHINESE_DECISION_THREADS", "16"))
    if threads < 1:
        raise laya_scores.InputError("--threads must be positive")
    torch.set_num_threads(threads)

    model_name = args.model or DEFAULT_MODEL
    local = Path(model_name).exists()
    load_options: dict[str, Any] = {"local_files_only": local}
    if args.revision:
        load_options["revision"] = args.revision
    device = args.device or ("cuda" if torch.cuda.is_available() else "cpu")
    dtype = torch.float32 if device == "cpu" else torch.bfloat16
    try:
        tokenizer = AutoTokenizer.from_pretrained(model_name, **load_options)
        model = AutoModelForCausalLM.from_pretrained(
            model_name,
            dtype=dtype,
            attn_implementation="sdpa",
            **load_options,
        ).to(device).eval()
    except Exception as exc:
        raise laya_scores.InputError(f"cannot load Chinese Decision model {model_name!r}: {exc}") from exc

    config = decision_config(model_name, args.revision)
    try:
        temperature = float(config["temperature"])
        configured_max_length = int(config["max_length"])
    except (KeyError, TypeError, ValueError) as exc:
        raise laya_scores.InputError("Chinese Decision config needs temperature and max_length") from exc
    max_length = args.max_len or configured_max_length
    if max_length < 1:
        raise laya_scores.InputError("--max-len must be positive")

    letter_ids = []
    for letter in LETTERS:
        ids = tokenizer.encode(letter, add_special_tokens=False)
        if len(ids) != 1:
            raise laya_scores.InputError(f"letter {letter!r} does not encode to one token")
        letter_ids.append(ids[0])
    model_device = next(model.parameters()).device

    def predict(state: Mapping[str, Any], questions: Mapping[str, Any]) -> Any:
        answers: dict[str, Any] = {}
        for question_id, question in questions.items():
            options = options_for(question)
            prompt = compile_prompt(tokenizer, state, question.get("instructions", ""), options)
            input_ids = tokenizer.encode(prompt, add_special_tokens=False)
            if len(input_ids) > max_length:
                raise laya_scores.InputError(
                    f"Chinese Decision input is {len(input_ids)} tokens; the limit is {max_length}"
                )
            with torch.inference_mode():
                logits = model(
                    torch.tensor([input_ids], device=model_device),
                    logits_to_keep=1,
                    use_cache=False,
                ).logits
            probabilities = calibrated_probabilities(
                logits[0, -1, letter_ids[: len(options)]].float().tolist(),
                temperature,
            )
            answers[question_id] = {
                "probabilities": {
                    option_id: probability
                    for (option_id, _), probability in zip(options, probabilities)
                }
            }
        return {"answers": answers, "usage": {"truncated": False}}

    metadata = {
        "provider": "chinese-decision",
        "group_size": args.group_size,
        "family": "Chinese Decision single-forward calibrated choice",
        "checkpoint": "chinese_decision_0_8b",
        "checkpoint_repo": DEFAULT_MODEL,
        "model": model_name,
        "package_version": getattr(transformers, "__version__", ""),
        "torch_version": getattr(torch, "__version__", ""),
        "temperature": temperature,
        "prompt_version": config.get("prompt_version", ""),
        "device": str(model_device),
        "threads": threads,
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
    parser.add_argument("--group-size", type=int, default=laya_scores.GROUP_SIZE)
    parser.add_argument("--model", default=None, help="checkpoint repository or local path")
    parser.add_argument("--device", default=None)
    parser.add_argument("--revision", default=None)
    parser.add_argument("--max-len", type=int, default=0)
    parser.add_argument("--threads", type=int, default=0)
    parser.add_argument("--skip-existing", action="store_true")
    return parser.parse_args(argv)


def main(
    argv: Sequence[str] | None = None,
    loader: Callable[[argparse.Namespace], tuple[Callable[[Mapping[str, Any], Mapping[str, Any]], Any], dict[str, Any]]] = decision_loader,
) -> int:
    return laya_scores.execute(parse_args(argv), loader)


if __name__ == "__main__":
    raise SystemExit(main())
