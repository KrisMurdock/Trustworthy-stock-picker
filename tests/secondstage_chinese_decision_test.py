#!/usr/bin/env python3
"""Unit checks for the offline Chinese Decision second-stage adapter."""

from __future__ import annotations

import json
import math
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_DIR = ROOT / "scripts" / "secondstage"
sys.path.insert(0, str(SCRIPT_DIR))

import chinese_decision_scores as decision
import laya_scores


class FakeTokenizer:
    def __init__(self):
        self.calls = []

    def apply_chat_template(self, messages, **kwargs):
        self.calls.append((messages, kwargs))
        return messages[0]["content"]


def candidates(count: int) -> list[dict]:
    return [
        {
            "code": f"sz{index:06d}",
            "name": f"股票{index}",
            "rank": index + 1,
            "_features": {
                "ma20_distance": index,
                "ma20_slope": index / 2,
                "return5": index - 5,
                "relative5": index / 3,
                "rsi14": 40 + index,
                "close_position": 0.5,
                "volume_ratio": 1.2,
                "high20_distance": -index,
                "atr14": 2.5,
                "upper_wick": 0.1,
            },
        }
        for index in range(count)
    ]


class ChineseDecisionScoresTest(unittest.TestCase):
    def test_choice_options_preserve_candidate_order(self):
        question = {
            "type": "choice",
            "instructions": "choose",
            "criteria": {"C2": "第二项", "C1": "第一项"},
        }
        self.assertEqual(
            decision.options_for(question),
            [("C2", "第二项"), ("C1", "第一项")],
        )

    def test_prompt_uses_lettered_candidates_and_disables_thinking(self):
        tokenizer = FakeTokenizer()
        prompt = decision.compile_prompt(
            tokenizer,
            {"as_of": "2026-10-20", "candidates": [{"id": "C1"}]},
            "Which candidate is strongest?",
            [("C1", "first"), ("C2", "second")],
        )
        self.assertIn("待判断的上下文", prompt)
        self.assertIn('"as_of":"2026-10-20"', prompt)
        self.assertIn("A: C1 — first", prompt)
        self.assertIn("B: C2 — second", prompt)
        self.assertIn("只返回候选项对应的字母", prompt)
        self.assertFalse(tokenizer.calls[0][1]["enable_thinking"])

    def test_calibrated_probabilities_are_normalized_and_finite(self):
        probabilities = decision.calibrated_probabilities([0.0, 0.0, 0.0], 1.108)
        self.assertEqual(len(probabilities), 3)
        self.assertAlmostEqual(sum(probabilities), 1.0, places=12)
        for probability in probabilities:
            self.assertTrue(math.isfinite(probability))
            self.assertAlmostEqual(probability, 1 / 3, places=12)
        with self.assertRaises(laya_scores.InputError):
            decision.calibrated_probabilities([0.0, 1.0], 0.0)
        with self.assertRaises(laya_scores.InputError):
            decision.calibrated_probabilities([0.0, math.nan], 1.0)

    def test_score_protocol_matches_laya_group_averaging(self):
        def predict(_state, questions):
            criteria = list(questions["rank"]["criteria"])
            weights = [1.0 / (index + 1) for index in range(len(criteria))]
            total = sum(weights)
            return {
                "answers": {
                    "rank": {
                        "probabilities": {
                            key: value / total for key, value in zip(criteria, weights)
                        }
                    }
                }
            }

        rows = candidates(4)
        adjacent, top12 = laya_scores.score_candidates(
            predict, rows, "2026-10-20", group_size=2
        )
        self.assertEqual(set(adjacent), {row["code"] for row in rows})
        self.assertEqual(set(top12), {row["code"] for row in rows[:2]})
        self.assertAlmostEqual(sum(top12.values()), 1.0, places=7)
        artifact = laya_scores.build_artifact(
            "2026-10-20",
            [row["code"] for row in rows],
            adjacent,
            top12,
            {"provider": "chinese-decision", "group_size": 2},
        )
        self.assertEqual(artifact["schema_version"], 1)
        self.assertEqual(artifact["model"]["provider"], "chinese-decision")
        self.assertEqual(set(artifact["scores"]), {row["code"] for row in rows})

    def test_local_decision_config_is_loaded(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "decision_config.json"
            path.write_text(
                json.dumps({"temperature": 1.1, "max_length": 1024}),
                encoding="utf-8",
            )
            config = decision.decision_config(directory, None)
            self.assertEqual(config["temperature"], 1.1)
            self.assertEqual(config["max_length"], 1024)

    def test_rejects_more_than_26_options(self):
        question = {
            "type": "choice",
            "instructions": "choose",
            "criteria": {f"C{index}": str(index) for index in range(27)},
        }
        with self.assertRaises(laya_scores.InputError):
            decision.options_for(question)


if __name__ == "__main__":
    unittest.main()
