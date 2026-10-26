#!/usr/bin/env python3
"""Unit checks for the DeepSeek two-stage evaluation metrics and fusion."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "secondstage" / "evaluate_deepseek.py"
spec = importlib.util.spec_from_file_location("evaluate_deepseek", SCRIPT)
assert spec and spec.loader
evaluate_deepseek = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evaluate_deepseek)


class DeepSeekEvaluationTests(unittest.TestCase):
    def test_percentile_is_tie_aware(self) -> None:
        self.assertEqual(evaluate_deepseek.percentile([]), [])
        self.assertEqual(evaluate_deepseek.percentile([5.0]), [0.5])
        self.assertEqual(evaluate_deepseek.percentile([10.0, 10.0, 20.0]), [0.25, 0.25, 1.0])

    def test_group_percentiles_use_adjacent_five(self) -> None:
        candidates = [{"code": code} for code in ("a", "b", "c", "d", "e")]
        result = evaluate_deepseek.group_percentiles(candidates, {"a": 1, "b": 1, "c": 2, "d": 3, "e": 4})
        self.assertEqual(result, {"a": 0.125, "b": 0.125, "c": 0.5, "d": 0.75, "e": 1.0})
        with self.assertRaisesRegex(evaluate_deepseek.InputError, "missing scores"):
            evaluate_deepseek.group_percentiles(candidates, {"a": 1})

    def test_fusion_weights_match_documented_protocol(self) -> None:
        candidates = [
            {"code": code, "blend_score": (index + 1) / 10, "rank": index + 1}
            for index, code in enumerate(("a", "b", "c", "d", "e"))
        ]
        deepseek = {"a": 10, "b": 20, "c": 30, "d": 40, "e": 50}
        jev = {"a": 50, "b": 40, "c": 30, "d": 20, "e": 10}
        tree = {"a": 0.5, "b": 0.6, "c": 0.7, "d": 0.8, "e": 0.9}
        scores = evaluate_deepseek.build_scores(candidates, deepseek, jev=jev, tree=tree)
        self.assertEqual(scores["deepseek_50"], {"a": 0.0, "b": 0.25, "c": 0.5, "d": 0.75, "e": 1.0})
        self.assertAlmostEqual(scores["deepseek_jev_40"]["a"], 0.4)
        self.assertAlmostEqual(scores["deepseek_jev_40"]["e"], 0.6)
        self.assertAlmostEqual(scores["deepseek_tree_40"]["a"], 0.0)
        self.assertAlmostEqual(scores["deepseek_tree_40"]["e"], 1.0)

    def test_top_metrics_use_descending_scores_and_ties_break_on_first_stage_rank(self) -> None:
        rows = []
        for index in range(12):
            rows.append({
                "first_stage_rank": index + 1,
                "ranker": float(12 - index),
                "actual": {
                    "touch_1pct": index < 3,
                    "max_high_return_pct": float(index),
                    "close3_return_pct": float(-index),
                },
            })
        result = evaluate_deepseek.metrics(rows, "ranker", universe_touches=5)
        self.assertAlmostEqual(result["hit@10"], 0.3)
        self.assertAlmostEqual(result["ndcg@10"], 1.0)
        self.assertAlmostEqual(result["max_high@10"], 4.5)
        self.assertAlmostEqual(result["close3@10"], -4.5)
        self.assertAlmostEqual(result["recall@10"], 0.6)
        self.assertAlmostEqual(result["hit@30"], 0.25)

    def test_score_artifact_rejects_wrong_date_and_out_of_range(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            valid = {
                "schema_version": 1,
                "as_of": "2026-10-21",
                "scores": {"a": {"adjacent15": 0.5, "full15_top12": 0.5}},
            }
            path = root / "valid.json"
            path.write_text(json.dumps(valid), encoding="utf-8")
            self.assertEqual(evaluate_deepseek.score_artifact(path, "2026-10-21"), {"a": 0.5})
            with self.assertRaisesRegex(evaluate_deepseek.InputError, "invalid score artifact"):
                evaluate_deepseek.score_artifact(path, "2026-10-22")
            valid["scores"]["a"]["adjacent15"] = 1.5
            path.write_text(json.dumps(valid), encoding="utf-8")
            with self.assertRaisesRegex(evaluate_deepseek.InputError, "invalid score"):
                evaluate_deepseek.score_artifact(path, "2026-10-21")


if __name__ == "__main__":
    unittest.main()
