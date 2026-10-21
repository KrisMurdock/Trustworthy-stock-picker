#!/usr/bin/env python3
"""Unit checks for the offline Laya second-stage scorer."""

from __future__ import annotations

import argparse
import importlib.util
import json
import math
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "secondstage" / "laya_scores.py"
spec = importlib.util.spec_from_file_location("laya_scores", SCRIPT)
assert spec and spec.loader
laya_scores = importlib.util.module_from_spec(spec)
spec.loader.exec_module(laya_scores)


def feature_row(index: int) -> dict:
    return {
        "return1": index / 10,
        "return5": index,
        "return10": index * 2,
        "rel_return5": index / 2,
        "ma20_slope": index / 4,
        "ma20_distance": index / 3,
        "atr14_pct": 2 + index / 10,
        "rsi14": 40 + index,
        "volume_ratio5": 1 + index / 20,
        "close_position": min(0.95, index / 40),
        "upper_wick": index / 100,
        "high20_distance": -index,
        "low20_distance": index,
        "touch_rate20": min(1, index / 30),
    }


def candidates(count: int) -> list[dict]:
    return [
        {"code": f"sz{index:06d}", "name": f"股票{index}", "rank": index + 1, "_features": feature_row(index)}
        for index in range(count)
    ]


class FakePredict:
    def __init__(self):
        self.calls = []

    def __call__(self, state, questions):
        self.calls.append(questions)
        criteria = list(questions["rank"]["criteria"])
        weights = [1.0 / (index + 1) for index in range(len(criteria))]
        total = sum(weights)
        return {"answers": {"rank": {"probabilities": {key: value / total for key, value in zip(criteria, weights)}}}}


class FakeBatchPredict(FakePredict):
    def __init__(self):
        super().__init__()
        self.batch_calls = []

    def batch(self, states, questions):
        self.batch_calls.append((states, questions))
        results = []
        for _state in states:
            answers = {}
            for qid, question in questions.items():
                criteria = list(question["criteria"])
                weights = [1.0 / (index + 1) for index in range(len(criteria))]
                total = sum(weights)
                answers[qid] = {
                    "probabilities": {
                        key: value / total for key, value in zip(criteria, weights)
                    }
                }
            results.append({"answers": answers, "usage": {"truncated": False}})
        return results


class LayaScoresTest(unittest.TestCase):
    def test_scores_all_groups_and_averages_three_orders(self):
        predict = FakePredict()
        rows = candidates(32)
        adjacent, top12 = laya_scores.score_candidates(predict, rows, "2026-10-21")

        self.assertEqual(len(predict.calls), 9)
        self.assertEqual(set(adjacent), {row["code"] for row in rows})
        self.assertEqual(set(top12), {row["code"] for row in rows[:15]})
        self.assertAlmostEqual(sum(top12.values()), 1.0, places=6)
        for value in adjacent.values():
            self.assertTrue(math.isfinite(value))
            self.assertGreaterEqual(value, 0.0)
            self.assertLessEqual(value, 1.0)

        first_group_orders = [
            list(call["rank"]["criteria"]) for call in predict.calls[:3]
        ]
        self.assertEqual(first_group_orders[1], list(reversed(first_group_orders[0])))
        self.assertEqual(first_group_orders[2], first_group_orders[0][1:] + first_group_orders[0][:1])

    def test_artifact_matches_go_contract(self):
        rows = candidates(20)
        predict = FakePredict()
        adjacent, top12 = laya_scores.score_candidates(predict, rows, "2026-10-21")
        artifact = laya_scores.build_artifact(
            "2026-10-21",
            [row["code"] for row in rows],
            adjacent,
            top12,
            {"provider": "laya", "family": "Laya", "model": "convaiinnovations/laya"},
        )
        self.assertEqual(artifact["schema_version"], 1)
        self.assertEqual(artifact["as_of"], "2026-10-21")
        self.assertEqual(artifact["prompt_version"], "second-stage-adjacent15-v1")
        self.assertEqual(artifact["model"]["provider"], "laya")
        self.assertEqual(set(artifact["scores"]), {row["code"] for row in rows})
        for score in artifact["scores"].values():
            self.assertEqual(set(score), {"adjacent15", "full15_top12"})

    def test_rejects_missing_features_before_model_load(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidates_path = root / "candidates.json"
            dataset_path = root / "dataset.json"
            candidates_path.write_text(json.dumps({"schema_version": 1, "dates": {"2026-10-21": [{"code": "sz000001"}]}}), encoding="utf-8")
            dataset_path.write_text(json.dumps({"schema_version": 1, "samples": []}), encoding="utf-8")
            with self.assertRaises(laya_scores.InputError):
                laya_scores.main(
                    ["--candidates", str(candidates_path), "--dataset", str(dataset_path), "--output-dir", str(root / "out")],
                    loader=lambda _args: (_ for _ in ()).throw(AssertionError("loader must not run")),
                )

    def test_rejects_bad_model_responses(self):
        cases = [
            {"answers": {}},
            {"answers": {"rank": {"probabilities": {}}}},
            {"answers": {"rank": {"probabilities": {"C1": 1.0}}}},
            {"answers": {"rank": {"probabilities": {"C1": -0.1, "C2": 1.1}}}},
            {"answers": {"rank": {"probabilities": {"C1": float("nan"), "C2": 1.0}}}},
            {"answers": {"rank": {"probabilities": {"C1": True, "C2": 1.0}}}},
        ]
        for response in cases:
            with self.subTest(response=response):
                with self.assertRaises(laya_scores.InputError):
                    laya_scores.score_candidates(lambda _state, _questions, response=response: response, candidates(2), "2026-10-21")

    def test_rejects_duplicate_codes(self):
        rows = candidates(2)
        rows[1]["code"] = rows[0]["code"]
        with self.assertRaises(laya_scores.InputError):
            laya_scores.score_candidates(FakePredict(), rows, "2026-10-21")

    def test_main_writes_artifacts_and_skips_existing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rows = [
                {"code": f"sz{index:06d}", "name": f"股票{index}", "rank": index + 1}
                for index in range(4)
            ]
            candidates_path = root / "candidates.json"
            dataset_path = root / "dataset.json"
            candidates_path.write_text(json.dumps({"schema_version": 1, "dates": {"2026-10-21": rows}}, ensure_ascii=False), encoding="utf-8")
            dataset_path.write_text(json.dumps({"schema_version": 1, "samples": [
                {"as_of": "2026-10-21", "code": row["code"], "features": feature_row(index)}
                for index, row in enumerate(rows)
            ]}), encoding="utf-8")
            output = root / "state"
            calls = []

            def loader(_args: argparse.Namespace):
                calls.append(1)
                return FakePredict(), {"provider": "laya", "family": "Laya", "model": "test"}

            argv = ["--candidates", str(candidates_path), "--dataset", str(dataset_path), "--output-dir", str(output)]
            self.assertEqual(laya_scores.main(argv, loader=loader), 0)
            artifact_path = output / "2026-10-21.json"
            artifact = json.loads(artifact_path.read_text(encoding="utf-8"))
            self.assertEqual(artifact["model"]["provider"], "laya")
            self.assertEqual(len(artifact["scores"]), 4)

            self.assertEqual(laya_scores.main(argv + ["--skip-existing"], loader=loader), 0)
            self.assertEqual(len(calls), 1)

    def test_batch_path_matches_sequential_and_records_group_size(self):
        rows = candidates(12)
        sequential = FakePredict()
        expected, _ = laya_scores.score_candidates(sequential, rows, "2026-10-21", group_size=5)
        batched = FakeBatchPredict()
        actual, _ = laya_scores.score_candidates(
            batched, rows, "2026-10-21", group_size=5, batch=batched.batch
        )
        self.assertEqual(set(actual), set(expected))
        for code in expected:
            self.assertAlmostEqual(actual[code], expected[code], places=7)
        self.assertEqual(len(batched.calls), 0)
        self.assertEqual(len(batched.batch_calls), 2)
        self.assertEqual(set(batched.batch_calls[0][1]), {"rank0", "rank1", "rank2"})
        self.assertEqual(set(batched.batch_calls[1][1]), {"rank0", "rank1", "rank2"})

    def test_rejects_truncated_batch_response(self):
        rows = candidates(5)
        def batch(states, questions):
            return [{"answers": {"rank0": {"probabilities": {"C1": 1.0, "C2": 0.0}}},
                     "usage": {"truncated": True}} for _ in states]
        with self.assertRaisesRegex(laya_scores.InputError, "truncated"):
            laya_scores.score_candidates(
                FakePredict(), rows, "2026-10-21", group_size=2, batch=batch
            )

    def test_artifact_carries_custom_prompt_version(self):
        rows = candidates(2)
        adjacent, top12 = laya_scores.score_candidates(FakePredict(), rows, "2026-10-21", group_size=2)
        artifact = laya_scores.build_artifact(
            "2026-10-21", [row["code"] for row in rows], adjacent, top12,
            {"provider": "laya"}, "second-stage-adjacent5-v1",
        )
        self.assertEqual(artifact["prompt_version"], "second-stage-adjacent5-v1")

    def test_rejects_too_few_candidates(self):
        with self.assertRaises(laya_scores.InputError):
            laya_scores.score_candidates(FakePredict(), candidates(1), "2026-10-21")

    def test_accepts_legacy_jev_feature_names(self):
        rows = [
            {"code": "sz000001", "_features": {
                "ma20_distance": 1, "ma20_slope": 2, "return5": 3, "relative5": 4,
                "rsi14": 5, "close_position": 0.5, "volume_ratio": 1.2,
                "high20_distance": -1, "atr14": 2.5, "upper_wick": 0.1,
            }},
            {"code": "sz000002", "_features": {
                "ma20_distance": 2, "ma20_slope": 1, "return5": 2, "relative5": 3,
                "rsi14": 6, "close_position": 0.6, "volume_ratio": 1.1,
                "high20_distance": -2, "atr14": 2.6, "upper_wick": 0.2,
            }},
        ]
        adjacent, _ = laya_scores.score_candidates(FakePredict(), rows, "2026-10-21")
        self.assertEqual(set(adjacent), {"sz000001", "sz000002"})


if __name__ == "__main__":
    unittest.main()
