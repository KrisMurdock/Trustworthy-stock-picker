#!/usr/bin/env python3
"""Compile fixed feature predicates into an aris-stock registration request."""
import argparse
import base64
import hashlib
import copy
import json
import re
from pathlib import Path

ID = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")


def load(path):
    with Path(path).open() as f:
        return json.load(f)


def parse_candidate(raw):
    name, sep, feature = raw.partition("=")
    if not sep or not ID.fullmatch(name) or not feature:
        raise ValueError("candidate must be NAME=FEATURE")
    return name, feature


def compile_request(args):
    shared_path = getattr(args, "shared_snapshot", None)
    if shared_path:
        base = {"schema_version": 3, "signals": load(args.signals)}
    else:
        base = load(args.base_input)
    features_bytes = Path(args.features).read_bytes()
    features = json.loads(features_bytes)
    constraints = load(args.constraints)
    inference = load(args.inference)
    budget = load(args.budget)
    if not isinstance(base, dict) or base.get("schema_version") != 3:
        raise ValueError("base input must be schema_version 3")
    signals = base.get("signals")
    if not isinstance(signals, list) or not signals:
        raise ValueError("base input must contain signals")
    if not isinstance(features, list):
        raise ValueError("features must be an array")
    by_key = {}
    for row in features:
        if not isinstance(row, dict) or not isinstance(row.get("code"), str) or not isinstance(row.get("date"), str):
            raise ValueError("feature rows require code and date")
        key = (row["code"], row["date"])
        if key in by_key:
            raise ValueError("duplicate feature row: %s %s" % key)
        by_key[key] = row
    keys = []
    seen = set()
    for signal in signals:
        key = (signal.get("code"), signal.get("date"))
        if key in seen:
            raise ValueError("duplicate base signal: %s %s" % key)
        if key not in by_key:
            raise ValueError("feature row missing for base signal: %s %s" % key)
        seen.add(key)
        keys.append(key)
    candidates = []
    names = set()
    for raw in args.candidate:
        name, feature = parse_candidate(raw)
        if name == "baseline" or name in names:
            raise ValueError("duplicate or reserved candidate: " + name)
        for key in keys:
            row = by_key[key]
            if feature not in row or (row[feature] is not None and type(row[feature]) is not bool):
                raise ValueError("feature must be present and boolean or null: " + feature)
        selected = [signal for signal, key in zip(signals, keys) if by_key[key].get(feature) is True]
        if not selected:
            raise ValueError("candidate has no signals: " + name)
        names.add(name)
        candidates.append((name, selected))
    if not candidates:
        raise ValueError("at least one candidate is required")
    if not all(isinstance(value, dict) for value in (constraints, inference, budget)):
        raise ValueError("constraints, inference and budget must be objects")
    inputs = {"baseline": copy.deepcopy(base)}
    for name, selected in candidates:
        candidate = copy.deepcopy(base)
        candidate["signals"] = selected
        inputs[name] = candidate
    result = {
        "request": {"schema_version": 1, "run_id": args.run_id, "strategy": "ma-proxy"},
        "human_contract": Path(args.human_contract).read_text(),
        "contract": {
            "schema_version": 1, "id": args.run_id + "-v1", "human_contract_sha256": "", "build_sha256": "",
            "baseline": "baseline", "candidates": [name for name, _ in candidates], "input_sha256": {},
            "constraints": constraints, "inference": inference, "budget": budget,
            "review": {"mode": "off", "require_cross_family": False},
        },
        "inputs": inputs,
    }
    specification = {"features_sha256": hashlib.sha256(features_bytes).hexdigest(),
                     "selectors": args.candidate}
    result["human_contract"] += "\nCompiler specification: " + json.dumps(specification, sort_keys=True) + "\n"
    if shared_path:
        compressed = Path(shared_path).read_bytes()
        if not 0 < len(compressed) <= 64 << 20:
            raise ValueError("shared snapshot compressed size exceeds 64 MiB")
        result["request"]["schema_version"] = 2
        result["shared"] = {
            "schema_version": 1, "snapshot_gzip": base64.b64encode(compressed).decode("ascii"),
            "snapshot_sha256": hashlib.sha256(compressed).hexdigest(),
            "sessions": load(args.sessions), "config": load(args.model_config),
            "signals": {name: value["signals"] for name, value in inputs.items()},
            "limit_mode": "historical_board",
        }
        del result["inputs"]
    return result


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    source = ap.add_mutually_exclusive_group(required=True)
    source.add_argument("--base-input")
    source.add_argument("--shared-snapshot")
    for name in ("sessions", "signals", "model-config"):
        ap.add_argument("--" + name)
    for name in ("features", "constraints", "inference", "budget", "human-contract", "run-id", "output"):
        ap.add_argument("--" + name, required=True)
    ap.add_argument("--candidate", action="append", default=[])
    args = ap.parse_args()
    if not ID.fullmatch(args.run_id) or len(args.run_id) > 61:
        ap.error("invalid run id")
    if args.shared_snapshot and not all((args.sessions, args.signals, args.model_config)):
        ap.error("shared snapshot requires sessions, signals and model-config")
    try:
        result = compile_request(args)
        with Path(args.output).open("x") as f:
            json.dump(result, f, ensure_ascii=False, separators=(",", ":"))
            f.write("\n")
    except (OSError, ValueError, json.JSONDecodeError) as error:
        ap.error(str(error))


if __name__ == "__main__":
    main()
