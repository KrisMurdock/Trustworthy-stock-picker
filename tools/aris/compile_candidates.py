#!/usr/bin/env python3
"""Compile fixed feature predicates into an aris-stock registration request."""
import argparse
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
    base = load(args.base_input)
    features = load(args.features)
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
        if feature not in by_key[keys[0]]:
            raise ValueError("feature not present: " + feature)
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
    return {
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


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    for name in ("base-input", "features", "constraints", "inference", "budget", "human-contract", "run-id", "output"):
        ap.add_argument("--" + name, required=True)
    ap.add_argument("--candidate", action="append", default=[])
    args = ap.parse_args()
    if not ID.fullmatch(args.run_id):
        ap.error("invalid run id")
    try:
        Path(args.output).write_text(json.dumps(compile_request(args), ensure_ascii=False, indent=2) + "\n")
    except (OSError, ValueError, json.JSONDecodeError) as error:
        ap.error(str(error))


if __name__ == "__main__":
    main()
