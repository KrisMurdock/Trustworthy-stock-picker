#!/usr/bin/env python3
"""Score first- or second-stage candidates with an OpenAI-compatible LLM.

The script consumes the same frozen candidate and feature inputs as the local
JEV/Laya workers. It emits the versioned second-stage artifact schema, so a
DeepSeek run can be compared with, or optionally published alongside, the
local models without changing the Go runtime contract.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import math
import os
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence


SCHEMA_VERSION = 1
GROUP_SIZE = 5
TOP_K_MASS = 12
DEFAULT_TIMEOUT = 90.0
DEFAULT_MAX_OUTPUT_TOKENS = 4096
DEFAULT_RETRIES = 3
DEFAULT_PARSE_RETRIES = 2
PROMPT_VERSION = "deepseek-adjacent-v1"
TEXT_LABELS = ("very low", "low", "low-to-mid", "mid", "mid-to-high", "high", "very high")
TEXT_CUTS = (0.10, 0.25, 0.40, 0.60, 0.75, 0.90)
COMPACT_FIELDS = (
    ("close_vs_ma20_pct", ("ma20_distance",), 1),
    ("ma20_slope_5d_pct", ("ma20_slope",), 1),
    ("return_5d_pct", ("return5",), 1),
    ("relative_strength_5d_pct", ("rel_return5", "relative_return5"), 1),
    ("rsi14", ("rsi14",), 0),
    ("close_position", ("close_position",), 2),
    ("volume_ratio", ("volume_ratio5", "volume_ratio"), 2),
    ("distance_to_20d_high_pct", ("high20_distance",), 1),
    ("atr14_pct", ("atr14_pct", "atr14"), 1),
    ("upper_wick", ("upper_wick",), 2),
)
SYSTEM_PROMPT = (
    "You are a point-in-time stock ranking model. Use only the supplied facts. "
    "Return one JSON object and no markdown, explanation, or reasoning text."
)
CRITERIA = {
    "stage1": (
        "Assign each candidate a 0-100 score for inclusion in a high-recall pool. "
        "Higher scores mean a better chance of a 3-trading-day maximum-high gain of "
        "at least 1% from the signal close, after accounting for reversal risk."
    ),
    "stage2": (
        "Assign each candidate a 0-100 score for a 3-trading-day maximum-high gain of "
        "at least 1% from the signal close, with higher scores for stronger continuation "
        "and lower reversal risk. Keep scores comparable within this candidate group."
    ),
}


class InputError(ValueError):
    """Raised when a request or response violates the scoring contract."""


class DeepSeekError(RuntimeError):
    """Raised when the upstream request cannot produce a valid result."""


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


def compact_candidate(features: Mapping[str, Any], candidate_id: str) -> dict[str, Any]:
    row: dict[str, Any] = {"id": candidate_id}
    for name, aliases, digits in COMPACT_FIELDS:
        row[name] = rounded(feature_value(features, *aliases), digits)
    return row


def percentile_ranks(values: Sequence[float]) -> list[float]:
    """Return tie-aware percentile ranks in [0, 1] for one date-wide field."""
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
        rank = ((start + end) / 2) / denominator
        for position in range(start, end + 1):
            result[order[position]] = rank
        start = end + 1
    return result


def text_label(percentile: float) -> str:
    for cut, label in zip(TEXT_CUTS, TEXT_LABELS):
        if percentile < cut:
            return label
    return TEXT_LABELS[-1]


def candidate_key(candidate: Mapping[str, Any]) -> str:
    value = candidate.get("code")
    if not isinstance(value, str) or not value.strip():
        value = candidate.get("id")
    if not isinstance(value, str) or not value.strip():
        raise InputError("candidate state must contain code or id")
    return value


def build_state_rows(
    prepared: Sequence[Mapping[str, Any]],
    as_of: str,
    profile: str,
    state_format: str,
) -> tuple[list[dict[str, Any]], dict[str, str]]:
    """Build model-visible candidate state and an option-id to stock-code map."""
    if profile != "compact":
        raise InputError("state profiles are legacy or compact; text requires compact")
    rows: list[dict[str, Any]] = []
    id_to_code: dict[str, str] = {}
    for index, item in enumerate(prepared, 1):
        candidate = item["candidate"]
        features = item["features"]
        candidate_id = f"C{index}"
        row = compact_candidate(features, candidate_id)
        rows.append(row)
        id_to_code[candidate_id] = str(candidate["code"])
    if state_format == "numeric":
        return rows, id_to_code
    if state_format != "text":
        raise InputError(f"unknown state format: {state_format}")
    for name, _, _ in COMPACT_FIELDS:
        values = [float(row[name]) for row in rows]
        labels = [text_label(rank) for rank in percentile_ranks(values)]
        for row, label in zip(rows, labels):
            row[name] = label
    return rows, id_to_code


def permutation_orders(count: int) -> list[list[int]]:
    if count < 2:
        return [list(range(count))]
    candidates = (
        list(range(count)),
        list(reversed(range(count))),
        list(range(1, count)) + [0],
    )
    result: list[list[int]] = []
    seen: set[tuple[int, ...]] = set()
    for order in candidates:
        key = tuple(order)
        if key not in seen:
            seen.add(key)
            result.append(order)
    return result


def build_prompt(
    stage: str,
    as_of: str,
    candidates: Sequence[Mapping[str, Any]],
    order: Sequence[int],
    state_format: str = "numeric",
    state_profile: str = "legacy",
) -> str:
    if stage not in CRITERIA:
        raise InputError(f"unknown stage: {stage}")
    ordered = [candidates[index] for index in order]
    payload = {
        "as_of": as_of,
        "criterion": CRITERIA[stage],
        "candidates": list(ordered),
        "output_schema": {
            "scores": [
                {"code": "candidate code", "score": "number from 0 to 100"},
            ]
        },
        "rules": [
            "Return exactly one score for every candidate code.",
            "Do not add or omit candidates.",
            "Use a spread of scores when evidence differs; ties are allowed.",
        ],
    }
    if state_format == "text":
        payload["state_format"] = "categorical"
        payload["state_note"] = (
            "Each candidate feature is a within-day cross-sectional level, not an exact value. "
            "Compare the level ordering and weigh the stated criterion."
        )
        payload["label_order"] = list(TEXT_LABELS)
    elif state_profile == "compact":
        payload["state_format"] = "numeric"
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":"))


def parse_score_content(content: str, expected_codes: Sequence[str]) -> dict[str, float]:
    text = content.strip()
    if text.startswith("```"):
        lines = text.splitlines()
        if lines and lines[0].startswith("```"):
            lines = lines[1:]
        if lines and lines[-1].strip() == "```":
            lines = lines[:-1]
        text = "\n".join(lines).strip()
    try:
        document = json.loads(text)
    except json.JSONDecodeError as exc:
        start = text.find("{")
        end = text.rfind("}")
        if start < 0 or end <= start:
            raise InputError("model response is not a JSON object") from exc
        try:
            document = json.loads(text[start : end + 1])
        except json.JSONDecodeError as nested:
            raise InputError("model response is not valid JSON") from nested
    if not isinstance(document, Mapping):
        raise InputError("model response must be a JSON object")
    raw_scores = document.get("scores")
    if isinstance(raw_scores, Mapping):
        items = [{"code": code, "score": value} for code, value in raw_scores.items()]
    elif isinstance(raw_scores, list):
        items = raw_scores
    else:
        raise InputError("model response is missing a scores object or list")
    scores: dict[str, float] = {}
    for item in items:
        if not isinstance(item, Mapping):
            raise InputError("model score entry must be an object")
        code = item.get("code")
        value = item.get("score")
        if not isinstance(code, str) or not code.strip():
            raise InputError("model score entry has no candidate code")
        try:
            number = float(value)
        except (TypeError, ValueError) as exc:
            raise InputError(f"model score for {code} is not numeric") from exc
        if not math.isfinite(number) or number < 0 or number > 100:
            raise InputError(f"model score for {code} is outside 0..100")
        if code in scores:
            raise InputError(f"model returned duplicate score for {code}")
        scores[code] = number / 100.0
    expected = set(expected_codes)
    if set(scores) != expected:
        missing = sorted(expected - set(scores))
        extra = sorted(set(scores) - expected)
        raise InputError(f"model score coverage mismatch; missing={missing[:5]} extra={extra[:5]}")
    return scores


def load_config(
    path: Path,
    base_url: str,
    model: str,
    api_key_env: str,
    effort: str = "",
) -> tuple[str, str, str, str]:
    values: dict[str, str] = {}
    if path.is_file():
        for line in path.read_text(encoding="utf-8").splitlines():
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or ":" not in stripped:
                continue
            key, value = stripped.split(":", 1)
            value = value.strip().strip("'\"")
            if key.strip() and value:
                values[key.strip()] = value
    selected_base = base_url.strip() or os.environ.get("DEEPSEEK_BASE_URL", "").strip() or values.get("base_url", "") or values.get("bash_url", "")
    selected_model = model.strip() or os.environ.get("DEEPSEEK_MODEL", "").strip() or values.get("model", "")
    selected_effort = effort.strip() or os.environ.get("DEEPSEEK_REASONING_EFFORT", "").strip() or values.get("effort", "") or values.get("reasoning_effort", "")
    selected_key = os.environ.get(api_key_env, "").strip() or values.get("api_key", "") or values.get("apikey", "")
    if not selected_base:
        raise InputError("DeepSeek base URL is missing")
    if not selected_model:
        raise InputError("DeepSeek model is missing")
    if not selected_key:
        raise InputError(f"DeepSeek API key is missing; set {api_key_env} or the private config file")
    parsed = urllib.parse.urlparse(selected_base)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise InputError("DeepSeek base URL must be an absolute HTTP(S) URL")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise InputError("DeepSeek base URL must not contain credentials, query, or fragment")
    if parsed.scheme == "http" and parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise InputError("DeepSeek HTTP is only allowed for loopback hosts")
    if selected_effort and selected_effort not in {"none", "minimal", "low", "medium", "high"}:
        raise InputError("DeepSeek reasoning effort must be none, minimal, low, medium, or high")
    return selected_base.rstrip("/"), selected_key, selected_model, selected_effort


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


class Client:
    def __init__(
        self,
        base_url: str,
        api_key: str,
        model: str,
        effort: str = "",
        timeout: float = DEFAULT_TIMEOUT,
        retries: int = DEFAULT_RETRIES,
        max_output_tokens: int = DEFAULT_MAX_OUTPUT_TOKENS,
        cache_dir: Path | None = None,
        transport: Callable[[urllib.request.Request, float], bytes] | None = None,
        sleeper: Callable[[float], None] = time.sleep,
    ) -> None:
        if timeout <= 0:
            raise InputError("timeout must be positive")
        if retries < 1:
            raise InputError("retries must be at least one")
        self.endpoint = base_url.rstrip("/") + "/chat/completions"
        self.api_key = api_key
        self.model = model
        self.effort = effort
        self.timeout = timeout
        self.retries = retries
        self.max_output_tokens = max_output_tokens
        self.cache_dir = cache_dir
        self.transport = transport
        self.sleeper = sleeper

    def _payload(self, prompt: str) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "model": self.model,
            "messages": [
                {"role": "system", "content": SYSTEM_PROMPT},
                {"role": "user", "content": prompt},
            ],
            "temperature": 0,
            "max_tokens": self.max_output_tokens,
            "response_format": {"type": "json_object"},
            "stream": False,
        }
        if self.effort:
            payload["reasoning_effort"] = self.effort
        return payload

    def _cache_path(self, payload: Mapping[str, Any]) -> Path | None:
        if self.cache_dir is None:
            return None
        encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
        return self.cache_dir / f"{hashlib.sha256(encoded).hexdigest()}.json"

    def _request(self, request: urllib.request.Request, timeout: float) -> bytes:
        if self.transport is not None:
            return self.transport(request, timeout)
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.read(2 * 1024 * 1024 + 1)

    def _post(self, prompt: str, bypass_cache: bool = False) -> tuple[str, Mapping[str, Any]]:
        payload = self._payload(prompt)
        cache_path = self._cache_path(payload)
        if not bypass_cache and cache_path is not None and cache_path.is_file():
            try:
                cached = json.loads(cache_path.read_text(encoding="utf-8"))
                return self._extract(cached, cached=True)
            except (OSError, json.JSONDecodeError, DeepSeekError):
                try:
                    cache_path.unlink()
                except OSError:
                    pass
        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        last_error: Exception | None = None
        for attempt in range(self.retries):
            request = urllib.request.Request(
                self.endpoint,
                data=encoded,
                headers={"Authorization": f"Bearer {self.api_key}", "Content-Type": "application/json"},
                method="POST",
            )
            try:
                body = self._request(request, self.timeout)
                if len(body) > 2 * 1024 * 1024:
                    raise DeepSeekError("DeepSeek response exceeds 2 MiB")
                document = json.loads(body.decode("utf-8"))
                result = self._extract(document, cached=False)
                if cache_path is not None:
                    write_json_atomic(cache_path, document)
                return result
            except urllib.error.HTTPError as exc:
                retryable = exc.code in {408, 409, 429} or exc.code >= 500
                last_error = DeepSeekError(f"DeepSeek request failed with HTTP {exc.code}")
                if not retryable or attempt + 1 >= self.retries:
                    raise last_error from exc
            except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
                last_error = DeepSeekError(f"DeepSeek request failed: {type(exc).__name__}")
                if attempt + 1 >= self.retries:
                    raise last_error from exc
            self.sleeper(float(2**attempt))
        raise last_error or DeepSeekError("DeepSeek request failed")

    @staticmethod
    def _extract(document: Any, cached: bool) -> tuple[str, Mapping[str, Any]]:
        if not isinstance(document, Mapping):
            raise DeepSeekError("DeepSeek response is not a JSON object")
        if document.get("error") is not None:
            raise DeepSeekError("DeepSeek returned an upstream error")
        choices = document.get("choices")
        if not isinstance(choices, list) or not choices or not isinstance(choices[0], Mapping):
            raise DeepSeekError("DeepSeek response has no completion choices")
        choice = choices[0]
        if choice.get("finish_reason") == "length":
            raise DeepSeekError("DeepSeek response hit the output-token limit")
        message = choice.get("message")
        if not isinstance(message, Mapping):
            raise DeepSeekError("DeepSeek response has no assistant message")
        content = message.get("content")
        if not isinstance(content, str) or not content.strip():
            raise DeepSeekError("DeepSeek returned empty content")
        usage = document.get("usage") if isinstance(document.get("usage"), Mapping) else {}
        return content, {**usage, "cache_hit": cached}

    def score_group(
        self,
        stage: str,
        as_of: str,
        candidates: Sequence[Mapping[str, Any]],
        state_format: str = "numeric",
        state_profile: str = "legacy",
    ) -> tuple[dict[str, float], list[Mapping[str, Any]]]:
        codes = [candidate_key(candidate) for candidate in candidates]
        if len(candidates) < 2:
            return {codes[0]: 0.5} if codes else {}, []
        by_code: dict[str, list[float]] = defaultdict(list)
        usages: list[Mapping[str, Any]] = []
        for order in permutation_orders(len(candidates)):
            prompt = build_prompt(stage, as_of, candidates, order, state_format, state_profile)
            last_error: InputError | None = None
            scores: dict[str, float] | None = None
            for attempt in range(DEFAULT_PARSE_RETRIES + 1):
                content, usage = self._post(prompt, bypass_cache=attempt > 0)
                try:
                    scores = parse_score_content(content, codes)
                    break
                except InputError as exc:
                    last_error = exc
                    cache_path = self._cache_path(self._payload(prompt))
                    if cache_path is not None:
                        try:
                            cache_path.unlink()
                        except OSError:
                            pass
                    if attempt >= DEFAULT_PARSE_RETRIES:
                        raise
                    self.sleeper(0.5 * (attempt + 1))
            if scores is None:
                raise last_error or InputError("model response could not be parsed")
            for code, score in scores.items():
                by_code[code].append(score)
            usages.append(usage)
        return {code: sum(values) / len(values) for code, values in by_code.items()}, usages


def load_inputs(candidates_path: Path, dataset_path: Path) -> tuple[dict[str, list[dict[str, Any]]], dict[tuple[str, str], Mapping[str, Any]]]:
    candidates_doc = json.loads(candidates_path.read_text(encoding="utf-8"))
    dataset = json.loads(dataset_path.read_text(encoding="utf-8"))
    dates = candidates_doc.get("dates")
    if candidates_doc.get("schema_version") != 1 or not isinstance(dates, Mapping):
        raise InputError("candidate document is invalid")
    features: dict[tuple[str, str], Mapping[str, Any]] = {}
    for sample in dataset.get("samples", []):
        if not isinstance(sample, Mapping):
            continue
        key = (str(sample.get("as_of", "")), str(sample.get("code", "")))
        if key[0] and key[1] and isinstance(sample.get("features"), Mapping):
            features[key] = sample["features"]
    result: dict[str, list[dict[str, Any]]] = {}
    for as_of, rows in dates.items():
        if not isinstance(rows, list) or not rows:
            continue
        prepared = []
        for row in sorted(rows, key=lambda item: (int(item.get("rank", 0)), str(item.get("code", "")))):
            if not isinstance(row, Mapping) or not isinstance(row.get("code"), str):
                raise InputError(f"candidate row for {as_of} is malformed")
            row_features = features.get((as_of, row["code"])) or row.get("features")
            if not isinstance(row_features, Mapping):
                raise InputError(f"missing features for {as_of} {row['code']}")
            prepared.append({"candidate": dict(row), "features": row_features})
        result[str(as_of)] = prepared
    if not result:
        raise InputError("no candidate dates were found")
    return result, features


def top_k_mass(scores: Mapping[str, float], codes: Sequence[str], limit: int) -> dict[str, float]:
    head = list(codes[:limit])
    total = sum(scores.get(code, 0.0) for code in head)
    if total <= 0:
        return {code: 0.0 for code in scores}
    return {code: (scores[code] / total if code in head else 0.0) for code in scores}


def score_date(
    client: Client,
    stage: str,
    as_of: str,
    candidates: Sequence[Mapping[str, Any]],
    group_size: int,
    workers: int,
    state_format: str = "numeric",
    state_profile: str = "legacy",
) -> tuple[dict[str, float], dict[str, Any]]:
    groups = [list(candidates[start : start + group_size]) for start in range(0, len(candidates), group_size)]
    scores: dict[str, float] = {}
    usage: dict[str, float] = defaultdict(float)
    requests = 0
    cache_hits = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, workers)) as executor:
        futures = [
            executor.submit(client.score_group, stage, as_of, group, state_format, state_profile)
            for group in groups
        ]
        for future in concurrent.futures.as_completed(futures):
            group_scores, group_usages = future.result()
            scores.update(group_scores)
            for item in group_usages:
                requests += 1
                cache_hits += int(bool(item.get("cache_hit")))
                for key in ("prompt_tokens", "completion_tokens", "total_tokens"):
                    try:
                        usage[key] += float(item.get(key, 0) or 0)
                    except (TypeError, ValueError):
                        pass
    expected = {candidate_key(candidate) for candidate in candidates}
    if set(scores) != expected:
        missing = sorted(expected - set(scores))
        raise InputError(f"DeepSeek did not score every candidate: {', '.join(missing[:5])}")
    usage.update({"requests": requests, "cache_hits": cache_hits})
    return scores, dict(usage)


def build_artifact(
    as_of: str,
    stage: str,
    scores: Mapping[str, float],
    codes: Sequence[str],
    metadata: Mapping[str, Any],
) -> dict[str, Any]:
    head_count = min(15, len(codes))
    head_codes = list(codes[:head_count])
    top12 = top_k_mass(scores, head_codes, min(TOP_K_MASS, head_count))
    return {
        "schema_version": SCHEMA_VERSION,
        "as_of": as_of,
        "scores": {
            code: {"adjacent15": scores[code], "full15_top12": top12.get(code, 0.0)}
            for code in codes
        },
        "prompt_version": f"{PROMPT_VERSION}-{stage}",
        "model": {**metadata, "stage": stage},
    }


def rank_candidates(candidates: Sequence[Mapping[str, Any]], scores: Mapping[str, float], top_n: int) -> list[dict[str, Any]]:
    ranked = sorted(candidates, key=lambda candidate: (-scores[str(candidate["code"])], int(candidate.get("rank", 0)), str(candidate["code"])))
    selected = []
    for index, candidate in enumerate(ranked[:top_n], 1):
        row = {key: value for key, value in candidate.items() if key != "_features"}
        row["rank"] = index
        selected.append(row)
    return selected


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidates", required=True, type=Path)
    parser.add_argument("--dataset", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--stage", choices=sorted(CRITERIA), required=True)
    parser.add_argument("--dates", default="", help="comma-separated as-of dates")
    parser.add_argument("--max-dates", type=int, default=0)
    parser.add_argument("--group-size", type=int, default=GROUP_SIZE)
    parser.add_argument("--workers", type=int, default=6)
    parser.add_argument("--config", type=Path, default=Path(os.environ.get("STOCK_WATCHER_DEEPSEEK_CONFIG_FILE", "llm.config")))
    parser.add_argument("--base-url", default="", help="override the private config base URL")
    parser.add_argument("--model", default="", help="override the private config model")
    parser.add_argument("--api-key-env", default="DEEPSEEK_API_KEY")
    parser.add_argument("--reasoning-effort", default="", help="override config effort: none, minimal, low, medium, or high")
    parser.add_argument("--state-profile", choices=("legacy", "compact"), default="legacy")
    parser.add_argument("--state-format", choices=("numeric", "text"), default="numeric")
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    parser.add_argument("--retries", type=int, default=DEFAULT_RETRIES)
    parser.add_argument("--max-output-tokens", type=int, default=DEFAULT_MAX_OUTPUT_TOKENS)
    parser.add_argument("--cache-dir", type=Path)
    parser.add_argument("--no-cache", action="store_true")
    parser.add_argument("--skip-existing", action="store_true")
    parser.add_argument("--top-out", type=Path, help="for stage1, write the selected candidates for stage2")
    parser.add_argument("--top-n", type=int, default=95)
    return parser.parse_args(argv)


def execute(args: argparse.Namespace, client: Client | None = None) -> int:
    if args.group_size < 2:
        raise InputError("--group-size must be at least 2")
    if args.workers < 1:
        raise InputError("--workers must be positive")
    if args.top_n < 1:
        raise InputError("--top-n must be positive")
    if args.state_format == "text" and args.state_profile != "compact":
        raise InputError("--state-format text requires --state-profile compact")
    candidates_by_date, _ = load_inputs(args.candidates, args.dataset)
    dates = sorted(candidates_by_date)
    if args.dates:
        wanted = {value.strip() for value in args.dates.split(",") if value.strip()}
        unknown = wanted - set(dates)
        if unknown:
            raise InputError(f"unknown candidate date: {', '.join(sorted(unknown))}")
        dates = [date for date in dates if date in wanted]
    if args.max_dates:
        dates = dates[: args.max_dates]
    if not dates:
        raise InputError("no candidate dates selected")
    if args.top_out and args.stage != "stage1":
        raise InputError("--top-out is only valid for stage1")
    pending = [date for date in dates if not (args.skip_existing and (args.output_dir / f"{date}.json").is_file())]
    if not pending:
        print("all selected dates already have artifacts", flush=True)
        return 0
    if client is None:
        base_url, api_key, model, effort = load_config(
            args.config, args.base_url, args.model, args.api_key_env, args.reasoning_effort,
        )
        client = Client(
            base_url=base_url,
            api_key=api_key,
            model=model,
            effort=effort,
            timeout=args.timeout,
            retries=args.retries,
            max_output_tokens=args.max_output_tokens,
            cache_dir=None if args.no_cache else (args.cache_dir or args.output_dir / "cache"),
        )
    metadata = {
        "provider": "deepseek",
        "model": client.model,
        "reasoning_effort": client.effort,
        "group_size": args.group_size,
        "permutations": len(permutation_orders(min(args.group_size, 10))),
        "timeout_seconds": client.timeout,
        "state_profile": args.state_profile,
        "state_format": args.state_format,
    }
    if args.state_format == "text":
        metadata["state_labels"] = list(TEXT_LABELS)
    started = time.perf_counter()
    total_usage: dict[str, float] = defaultdict(float)
    for index, as_of in enumerate(pending, 1):
        prepared = candidates_by_date[as_of]
        candidates = [dict(item["candidate"], _features=item["features"]) for item in prepared]
        if args.state_profile == "compact":
            state_rows, id_to_code = build_state_rows(prepared, as_of, args.state_profile, args.state_format)
        else:
            state_rows = candidates
            id_to_code = {str(candidate["code"]): str(candidate["code"]) for candidate in candidates}
        scores_by_id, usage = score_date(
            client, args.stage, as_of, state_rows, args.group_size, args.workers,
            args.state_format, args.state_profile,
        )
        scores = {id_to_code[state_id]: score for state_id, score in scores_by_id.items()}
        for key, value in usage.items():
            total_usage[key] += float(value)
        codes = [str(candidate["code"]) for candidate in candidates]
        artifact = build_artifact(as_of, args.stage, scores, codes, {**metadata, "usage": usage})
        output = args.output_dir / f"{as_of}.json"
        write_json_atomic(output, artifact)
        if args.top_out:
            next_rows = rank_candidates(candidates, scores, args.top_n)
            document = json.loads(args.top_out.read_text(encoding="utf-8")) if args.top_out.is_file() else {
                "schema_version": 1,
                "method": "deepseek_stage1",
                "budget": 0,
                "dates": {},
            }
            document["dates"][as_of] = next_rows
            document["budget"] = len(next_rows)
            write_json_atomic(args.top_out, document)
        print(
            f"{index}/{len(pending)} {as_of} candidates={len(candidates)} scored={len(artifact['scores'])} "
            f"elapsed={time.perf_counter() - started:.1f}s -> {output}",
            flush=True,
        )
    print(f"usage={dict(total_usage)}", flush=True)
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    return execute(parse_args(argv))


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (InputError, DeepSeekError) as error:
        print(f"ERROR: {error}", flush=True)
        raise SystemExit(1)
