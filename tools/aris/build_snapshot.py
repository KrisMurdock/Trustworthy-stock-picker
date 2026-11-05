#!/usr/bin/env python3
"""Build an aris-stock snapshot from the frozen history-expansion cache."""
import argparse
import gzip
import json
from pathlib import Path


def read_json(path):
    with Path(path).open() as f:
        return json.load(f)


def build(histories, capacity, signals, sessions, max_codes=None):
    codes = sorted({s["code"] for s in signals})
    if max_codes:
        codes = codes[:max_codes]
    selected = set(codes)
    stocks = {}
    for code in codes:
        with gzip.open(Path(histories) / (code + ".json.gz"), "rt") as f:
            history = json.load(f)
        qfq = [["qfq", b["date"], round(b["open"] * 1e6), round(b["high"] * 1e6), round(b["low"] * 1e6), round(b["close"] * 1e6), b["volume_raw"], history["source"]] for b in history["bars"]]
        raw_path = Path(capacity) / (code + "-capacity.json.gz")
        if not raw_path.exists():
            raise ValueError("capacity cache missing: " + code)
        with gzip.open(raw_path, "rt") as f:
            raw = json.load(f)
        none = [["none", date, close, close, close, close, volume, source] for date, close, volume, source in raw]
        stocks[code] = qfq + none
    kept = [s for s in signals if s["code"] in selected]
    return {"schema_version": 1, "calendar": sessions, "stocks": stocks}, kept


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--histories", required=True)
    ap.add_argument("--capacity", required=True)
    ap.add_argument("--signals", required=True)
    ap.add_argument("--sessions", required=True)
    ap.add_argument("--max-codes", type=int)
    ap.add_argument("--output", required=True)
    args = ap.parse_args()
    signals = read_json(args.signals)
    calendar = read_json(args.sessions)
    if isinstance(calendar, dict):
        calendar = calendar["sessions"]
    snapshot, kept = build(args.histories, args.capacity, signals, calendar, args.max_codes)
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    with gzip.open(output, "wt") as f:
        json.dump(snapshot, f, ensure_ascii=False, separators=(",", ":"))
    output.with_suffix(".signals.json").write_text(json.dumps(kept, ensure_ascii=False, indent=2) + "\n")


if __name__ == "__main__":
    main()
