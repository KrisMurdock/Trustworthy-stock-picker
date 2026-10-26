#!/usr/bin/env python3
"""Generate all daily second-stage artifacts after the close.

The production Go process never calls this script. A post-close systemd timer
waits for the frozen first-stage report, writes candidate/feature inputs, then
runs the configured offline decision models in parallel. Artifacts are
published atomically only when every selected model succeeded.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
from contextlib import closing
from datetime import date, datetime, timedelta
from pathlib import Path
from typing import Any, Mapping, Sequence
from zoneinfo import ZoneInfo


SHANGHAI = ZoneInfo("Asia/Shanghai")
SCRIPT_DIR = Path(__file__).resolve().parent
REPOSITORY_ROOT = SCRIPT_DIR.parents[1]
CONFIGURED_DATA_DIR = Path(os.environ.get("STOCK_WATCHER_DATA_DIR", "/mnt/windows_database"))
DATA_DIR = CONFIGURED_DATA_DIR if CONFIGURED_DATA_DIR.is_absolute() else (REPOSITORY_ROOT / CONFIGURED_DATA_DIR)
DEFAULT_DATABASE = DATA_DIR / "market.db"
DEFAULT_STATE_ROOT = DATA_DIR / "state" / "secondstage"
DEFAULT_FEATURES_ROOT = DATA_DIR / "build_features"
DEFAULT_MODELS_ROOT = DATA_DIR / "download_models"
DEFAULT_PYTHON_ENV = Path("/root/python_env/blend-eval")
MODEL_NAMES = ("jev", "laya_official", "laya_multilingual", "chinese_decision")
DEEPSEEK_MODEL = "deepseek"


class PipelineError(RuntimeError):
    """Raised when a daily artifact cannot be produced safely."""


def log(message: str) -> None:
    print(f"[{datetime.now(SHANGHAI).isoformat(timespec='seconds')}] {message}", flush=True)


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--as-of", help="signal date; defaults to today's completed session")
    parser.add_argument("--database", type=Path, default=DEFAULT_DATABASE)
    parser.add_argument("--state-root", type=Path, default=DEFAULT_STATE_ROOT)
    parser.add_argument("--features-root", type=Path, default=DEFAULT_FEATURES_ROOT)
    parser.add_argument("--models-root", type=Path, default=DEFAULT_MODELS_ROOT)
    parser.add_argument("--python", default=sys.executable, help="Python interpreter for model workers")
    parser.add_argument("--python-env", type=Path, default=DEFAULT_PYTHON_ENV)
    parser.add_argument("--group-size", type=int, default=5)
    parser.add_argument("--jobs", type=int, default=4)
    parser.add_argument("--threads", type=int, default=8)
    parser.add_argument("--laya-max-len", type=int, default=4096, help="Laya state token budget")
    parser.add_argument("--laya-head-max-len", type=int, default=256, help="Laya question/head token budget")
    parser.add_argument("--deadline", default="15:28", help="latest publication time, HH:MM")
    parser.add_argument("--wait-interval", type=float, default=5.0)
    parser.add_argument("--max-runtime", type=float, default=1200.0, help="model runtime cap when no deadline applies")
    parser.add_argument("--no-wait", action="store_true", help="fail immediately when today's report is absent")
    parser.add_argument("--skip-existing", action="store_true", help="exit when all selected date artifacts already exist")
    parser.add_argument("--prepare-only", action="store_true", help="build candidate/feature inputs only")
    parser.add_argument("--deepseek", action="store_true", help="also generate the optional DeepSeek second-stage artifact")
    parser.add_argument("--deepseek-config", type=Path, default=Path(os.environ.get("STOCK_WATCHER_DEEPSEEK_CONFIG_FILE", REPOSITORY_ROOT / "llm.config")))
    parser.add_argument("--deepseek-model", default="", help="override the model named in the DeepSeek config file")
    parser.add_argument("--deepseek-workers", type=int, default=6)
    return parser.parse_args(argv)


def database_connection(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise PipelineError(f"market database is missing: {path}")
    connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=10)
    connection.row_factory = sqlite3.Row
    return connection


def is_trading_day(path: Path, as_of: str) -> bool:
    with closing(database_connection(path)) as connection:
        row = connection.execute("SELECT 1 FROM market_calendar WHERE trade_date=?", (as_of,)).fetchone()
        return row is not None


def load_report(path: Path, as_of: str) -> dict[str, Any] | None:
    with closing(database_connection(path)) as connection:
        row = connection.execute("SELECT report_json FROM selection_run WHERE as_of=?", (as_of,)).fetchone()
        if row is None:
            return None
        raw = row["report_json"]
        if isinstance(raw, memoryview):
            raw = raw.tobytes()
        if isinstance(raw, bytes):
            raw = raw.decode("utf-8")
        return json.loads(raw)


def deadline_for(as_of: str, value: str, now: datetime) -> datetime | None:
    if not value:
        return None
    try:
        clock = datetime.strptime(value, "%H:%M").time()
    except ValueError as exc:
        raise PipelineError("--deadline must use HH:MM") from exc
    target = datetime.combine(date.fromisoformat(as_of), clock, SHANGHAI)
    if as_of != now.date().isoformat():
        return None
    return target


def wait_for_report(args: argparse.Namespace, as_of: str, deadline: datetime | None) -> dict[str, Any]:
    while True:
        report = load_report(args.database, as_of)
        if report is not None:
            first = report.get("first_stage") or {}
            if first.get("status") != "ok" or not first.get("candidates"):
                raise PipelineError(f"report {as_of} has no successful first-stage candidates")
            if first.get("as_of") != as_of:
                raise PipelineError(f"report first-stage date is {first.get('as_of')}, expected {as_of}")
            return report
        now = datetime.now(SHANGHAI)
        if args.no_wait:
            raise PipelineError(f"report {as_of} is not available")
        if deadline is not None and now >= deadline:
            raise PipelineError(f"report {as_of} did not arrive before {deadline.strftime('%H:%M')}")
        log(f"waiting for frozen report {as_of}")
        time.sleep(max(0.5, args.wait_interval))


def first_stage_candidates(report: Mapping[str, Any], as_of: str) -> list[dict[str, Any]]:
    first = report.get("first_stage") or {}
    candidates = first.get("candidates") or []
    if not isinstance(candidates, list) or not candidates:
        raise PipelineError("first-stage candidate list is empty")
    ordered = []
    for index, item in enumerate(candidates, 1):
        if not isinstance(item, dict) or not isinstance(item.get("code"), str):
            raise PipelineError("first-stage candidate is malformed")
        features = item.get("features")
        if not isinstance(features, dict):
            raise PipelineError(f"candidate {item['code']} has no feature snapshot")
        ordered.append({
            "code": item["code"],
            "name": item.get("name", ""),
            "rank": int(item.get("rank", index)),
            "base_score": float(item.get("base_score", 0.0)),
            "model_score": float(item.get("model_score", 0.0)),
            "base_rank": float(item.get("base_rank", 0.0)),
            "model_rank": float(item.get("model_rank", 0.0)),
            "blend_score": float(item.get("blend_score", 0.0)),
            "features": dict(features),
        })
    ordered.sort(key=lambda row: (row["rank"], row["code"]))
    return ordered


def build_inputs(report: Mapping[str, Any], as_of: str, output: Path) -> tuple[Path, Path]:
    candidates = first_stage_candidates(report, as_of)
    feature_keys = sorted({key for item in candidates for key in item["features"]})
    samples = []
    for item in candidates:
        features = dict(item["features"])
        if "relative_return5" in features and "rel_return5" not in features:
            features["rel_return5"] = features["relative_return5"]
        if "volume_ratio" in features and "volume_ratio5" not in features:
            features["volume_ratio5"] = features["volume_ratio"]
        samples.append({
            "as_of": as_of,
            "code": item["code"],
            "name": item["name"],
            "base_score": item["base_score"],
            "features": features,
        })
    candidate_doc = {
        "schema_version": 1,
        "method": "blend",
        "budget": len(candidates),
        "dates": {as_of: candidates},
    }
    dataset = {
        "schema_version": 1,
        "dates": [as_of],
        "feature_keys": feature_keys,
        "samples": samples,
    }
    candidates_path = output / "candidates_stage2_top95.json"
    dataset_path = output / "dataset.json"
    write_json(candidates_path, candidate_doc)
    write_json(dataset_path, dataset)
    write_json(output / "report.json", report)
    return candidates_path, dataset_path


def write_json(path: Path, payload: Mapping[str, Any]) -> None:
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


def atomic_publish(source: Path, target: Path) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
    os.close(descriptor)
    try:
        shutil.copyfile(source, temporary)
        os.chmod(temporary, 0o600)
        with open(temporary, "rb") as handle:
            os.fsync(handle.fileno())
        os.replace(temporary, target)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def worker_environment(args: argparse.Namespace) -> dict[str, str]:
    env = dict(os.environ)
    python_path = [str(SCRIPT_DIR)]
    if args.python_env.is_dir():
        python_path.insert(0, str(args.python_env))
    if env.get("PYTHONPATH"):
        python_path.append(env["PYTHONPATH"])
    env["PYTHONPATH"] = os.pathsep.join(python_path)
    env["HF_HUB_OFFLINE"] = "1"
    env["TRANSFORMERS_OFFLINE"] = "1"
    env["TOKENIZERS_PARALLELISM"] = "false"
    env["OMP_NUM_THREADS"] = str(args.threads)
    env["MKL_NUM_THREADS"] = str(args.threads)
    env["LAYA_THREADS"] = str(args.threads)
    env["CHINESE_DECISION_THREADS"] = str(args.threads)
    env["JEV_THREADS"] = str(args.threads)
    return env


def run_worker(name: str, command: Sequence[str], env: Mapping[str, str], log_path: Path, timeout: float) -> None:
    log_path.parent.mkdir(parents=True, exist_ok=True)
    started = time.perf_counter()
    log(f"starting {name}: timeout={timeout:.0f}s")
    with log_path.open("w", encoding="utf-8") as handle:
        try:
            completed = subprocess.run(command, stdout=handle, stderr=subprocess.STDOUT, env=dict(env), timeout=max(1.0, timeout))
        except subprocess.TimeoutExpired as exc:
            raise PipelineError(f"{name} exceeded its deadline") from exc
    if completed.returncode != 0:
        raise PipelineError(f"{name} failed with exit code {completed.returncode}; see {log_path}")
    log(f"completed {name} in {time.perf_counter() - started:.1f}s")


def model_commands(args: argparse.Namespace, as_of: str, candidates: Path, dataset: Path, scores_root: Path) -> dict[str, tuple[list[str], Path]]:
    base = [args.python]
    laya = SCRIPT_DIR / "laya_scores.py"
    chinese = SCRIPT_DIR / "chinese_decision_scores.py"
    jev = SCRIPT_DIR / "jev_scores.py"
    common = [str(candidates), str(dataset), "--output-dir"]
    commands = {
        "laya_official": ([
            *base, str(laya), "--candidates", common[0], "--dataset", common[1], "--output-dir", str(scores_root / "laya_official"),
            "--dates", as_of, "--group-size", str(args.group_size), "--checkpoint", "official", "--model", str(args.models_root / "laya" / "official"), "--device", "cpu",
            "--max-len", str(args.laya_max_len), "--head-max-len", str(args.laya_head_max_len),
        ], scores_root / "laya_official" / f"{as_of}.json"),
        "laya_multilingual": ([
            *base, str(laya), "--candidates", common[0], "--dataset", common[1], "--output-dir", str(scores_root / "laya_multilingual"),
            "--dates", as_of, "--group-size", str(args.group_size), "--checkpoint", "multilingual", "--model", str(args.models_root / "laya" / "multilingual"), "--device", "cpu",
            "--max-len", str(args.laya_max_len), "--head-max-len", str(args.laya_head_max_len),
        ], scores_root / "laya_multilingual" / f"{as_of}.json"),
        "chinese_decision": ([
            *base, str(chinese), "--candidates", common[0], "--dataset", common[1], "--output-dir", str(scores_root / "chinese_decision"),
            "--dates", as_of, "--group-size", str(args.group_size), "--model", str(args.models_root / "chinese-decision" / "chinese-decision-0.8b"), "--device", "cpu", "--threads", str(args.threads),
        ], scores_root / "chinese_decision" / f"{as_of}.json"),
        "jev": ([
            *base, str(jev), "--candidates", common[0], "--dataset", common[1], "--output-dir", str(scores_root / "jev"),
            "--as-of", as_of, "--group-size", str(args.group_size), "--model", str(args.models_root / "jev" / "Qwen3-0.6B"), "--threads", str(args.threads),
        ], scores_root / "jev" / f"{as_of}.json"),
    }
    if getattr(args, "deepseek", False):
        deepseek = SCRIPT_DIR / "deepseek_scores.py"
        command = [
            *base, str(deepseek), "--candidates", common[0], "--dataset", common[1],
            "--output-dir", str(scores_root / DEEPSEEK_MODEL), "--stage", "stage2",
            "--dates", as_of, "--group-size", str(args.group_size),
            "--workers", str(getattr(args, "deepseek_workers", 6)),
            "--reasoning-effort", "none",
            "--config", str(args.deepseek_config),
        ]
        if getattr(args, "deepseek_model", ""):
            command.extend(["--model", args.deepseek_model])
        commands[DEEPSEEK_MODEL] = (command, scores_root / DEEPSEEK_MODEL / f"{as_of}.json")
    return commands


def selected_models(args: argparse.Namespace) -> tuple[str, ...]:
    if getattr(args, "deepseek", False):
        return (*MODEL_NAMES, DEEPSEEK_MODEL)
    return MODEL_NAMES


def all_artifacts_exist(state_root: Path, as_of: str, models: Sequence[str] = MODEL_NAMES) -> bool:
    return all((state_root / name / f"{as_of}.json").is_file() for name in models)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    models = selected_models(args)
    if args.group_size < 2:
        raise PipelineError("--group-size must be at least 2")
    if args.jobs < 1 or args.jobs > len(models):
        raise PipelineError(f"--jobs must be between 1 and {len(models)}")
    if args.threads < 1:
        raise PipelineError("--threads must be positive")
    if getattr(args, "deepseek_workers", 1) < 1:
        raise PipelineError("--deepseek-workers must be positive")
    if args.laya_max_len < 512 or args.laya_head_max_len < 64:
        raise PipelineError("Laya token budgets must leave room for the candidate state and question")
    now = datetime.now(SHANGHAI)
    as_of = args.as_of or now.date().isoformat()
    if args.as_of is None and not is_trading_day(args.database, as_of):
        log(f"{as_of} is not a stored trading day; no artifacts required")
        return 0
    if args.skip_existing and all_artifacts_exist(args.state_root, as_of, models):
        log(f"all artifacts already exist for {as_of}")
        return 0
    deadline = deadline_for(as_of, args.deadline, now)
    if deadline is not None and now >= deadline:
        raise PipelineError(f"artifact generation started after {deadline.strftime('%H:%M')}")
    report = wait_for_report(args, as_of, deadline)
    run_id = f"postclose-{as_of}-{now.strftime('%Y%m%d-%H%M%S')}"
    run_root = args.features_root / "reranking" / run_id
    log(f"building inputs for {as_of} in {run_root}")
    candidates, dataset = build_inputs(report, as_of, run_root / "inputs")
    if args.prepare_only:
        log(f"prepared {run_root}")
        return 0
    commands = model_commands(args, as_of, candidates, dataset, run_root / "scores")
    for name, (_, output) in commands.items():
        if not output.parent.exists():
            output.parent.mkdir(parents=True, exist_ok=True)
    hard_deadline = deadline if deadline is not None else datetime.now(SHANGHAI) + timedelta(seconds=args.max_runtime)
    errors: list[str] = []
    manifest: dict[str, Any] = {
        "schema_version": 1,
        "run_id": run_id,
        "as_of": as_of,
        "report_id": report.get("id", ""),
        "input_id": report.get("input_id", ""),
        "report_revision": report.get("revision", ""),
        "group_size": args.group_size,
        "jobs": args.jobs,
        "threads": args.threads,
        "laya_max_len": args.laya_max_len,
        "laya_head_max_len": args.laya_head_max_len,
        "started_at": now.isoformat(),
        "models": {},
    }
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as executor:
        futures = {}
        for name, (command, output) in commands.items():
            remaining = (hard_deadline - datetime.now(SHANGHAI)).total_seconds() - 20.0
            if remaining < 10:
                raise PipelineError(f"not enough time remains for {name}")
            futures[executor.submit(run_worker, name, command, worker_environment(args), run_root / "logs" / f"{name}.log", remaining)] = name
        for future in concurrent.futures.as_completed(futures):
            name = futures[future]
            try:
                future.result()
                output = commands[name][1]
                if not output.is_file():
                    raise PipelineError(f"{name} produced no artifact at {output}")
                manifest["models"][name] = {"status": "ok", "artifact": str(output), "sha256": sha256(output)}
            except Exception as exc:  # keep waiting for other workers to report their status
                manifest["models"][name] = {"status": "failed", "error": str(exc)}
                errors.append(f"{name}: {exc}")
    manifest["finished_at"] = datetime.now(SHANGHAI).isoformat()
    write_json(run_root / "manifest.json", manifest)
    if errors:
        raise PipelineError("; ".join(errors))
    published: dict[str, str] = {}
    for name in models:
        output = commands[name][1]
        target = args.state_root / name / f"{as_of}.json"
        atomic_publish(output, target)
        published[name] = str(target)
    write_json(run_root / "published.json", {"as_of": as_of, "artifacts": published})
    log(f"published {len(published)} artifacts for {as_of}: {', '.join(published)}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except PipelineError as error:
        log(f"ERROR: {error}")
        raise SystemExit(1)
