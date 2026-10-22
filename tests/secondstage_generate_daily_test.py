#!/usr/bin/env python3
"""Unit checks for the post-close second-stage artifact orchestrator."""

from __future__ import annotations

import argparse
import importlib.util
import json
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "secondstage" / "generate_daily.py"
spec = importlib.util.spec_from_file_location("generate_daily", SCRIPT)
assert spec and spec.loader
generate_daily = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generate_daily)


def feature_snapshot(offset: float) -> dict[str, float]:
    return {
        "ma20_distance": 2 + offset,
        "ma20_slope": 1 + offset,
        "return5": 3 + offset,
        "relative_return5": 4 + offset,
        "rsi14": 55 + offset,
        "close_position": 0.7,
        "volume_ratio": 1.2,
        "high20_distance": -5,
        "atr14_pct": 2.5,
        "upper_wick": 0.1,
    }


def report() -> dict:
    return {
        "id": "report-id",
        "input_id": "input-id",
        "first_stage": {
            "status": "ok",
            "as_of": "2026-10-21",
            "candidates": [
                {"code": "sz000002", "rank": 2, "features": feature_snapshot(1)},
                {"code": "sz000001", "rank": 1, "features": feature_snapshot(0)},
            ],
        },
    }


class GenerateDailyTests(unittest.TestCase):
    def test_build_inputs_preserves_frozen_rank_order(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            candidates_path, dataset_path = generate_daily.build_inputs(report(), "2026-10-21", Path(temporary))
            candidates = json.loads(candidates_path.read_text(encoding="utf-8"))
            dataset = json.loads(dataset_path.read_text(encoding="utf-8"))
            self.assertEqual([row["code"] for row in candidates["dates"]["2026-10-21"]], ["sz000001", "sz000002"])
            self.assertEqual([row["code"] for row in dataset["samples"]], ["sz000001", "sz000002"])
            self.assertEqual(dataset["samples"][0]["features"]["rel_return5"], 4)

    def test_laya_commands_use_explicit_token_budgets(self) -> None:
        args = argparse.Namespace(
            python="/usr/bin/python3.11",
            models_root=Path("/models"),
            group_size=5,
            threads=8,
            laya_max_len=4096,
            laya_head_max_len=256,
        )
        commands = generate_daily.model_commands(args, "2026-10-21", Path("candidates.json"), Path("dataset.json"), Path("scores"))
        for name in ("laya_official", "laya_multilingual"):
            command = commands[name][0]
            self.assertEqual(command[command.index("--max-len") + 1], "4096")
            self.assertEqual(command[command.index("--head-max-len") + 1], "256")

    def test_deadline_only_applies_to_current_signal_date(self) -> None:
        shanghai = ZoneInfo("Asia/Shanghai")
        now = datetime(2026, 10, 21, 15, 10, tzinfo=shanghai)
        self.assertEqual(generate_daily.deadline_for("2026-10-21", "15:28", now), datetime(2026, 10, 21, 15, 28, tzinfo=shanghai))
        self.assertIsNone(generate_daily.deadline_for("2026-10-20", "15:28", now))
        self.assertIsNone(generate_daily.deadline_for("2026-10-21", "", now))


if __name__ == "__main__":
    unittest.main()
