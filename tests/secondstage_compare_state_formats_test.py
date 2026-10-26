#!/usr/bin/env python3
"""Unit checks for the numeric-vs-text state comparison report."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts" / "secondstage"
sys.path.insert(0, str(SCRIPTS))
SCRIPT = SCRIPTS / "compare_state_formats.py"
spec = importlib.util.spec_from_file_location("compare_state_formats", SCRIPT)
assert spec and spec.loader
compare_state_formats = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compare_state_formats)


def metrics(value: float) -> dict[str, float]:
    return {
        "recall@10": value,
        "recall@30": value,
        "recall@95": value,
        "hit@10": value,
        "hit@30": value,
        "ndcg@10": value,
        "ndcg@30": value,
        "max_high@10": value,
        "max_high@30": value,
        "close3@10": value,
        "close3@30": value,
    }


def comparison(numeric: float, text: float, dates: tuple[str, ...] = ("2026-10-01", "2026-10-02")) -> dict:
    def per_date(key: str) -> dict:
        return {date: {key: metrics(numeric), "baseline": metrics(0.0)} for date in dates}

    return {
        "protocol": {"dates": list(dates)},
        "stage2": {
            "per_date": {
                date: {
                    "baseline": metrics(0.1),
                    "tree": metrics(0.2),
                    "jev": metrics(0.3),
                    "deepseek": metrics(numeric),
                }
                for date in dates
            }
        },
    }


class CompareStateFormatsTests(unittest.TestCase):
    def write(self, directory: Path, name: str, document: dict) -> Path:
        path = directory / name
        path.write_text(json.dumps(document), encoding="utf-8")
        return path

    def test_text_minus_numeric_delta_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            numeric = comparison(0.4, 0.4)
            text = comparison(0.6, 0.6)
            numeric_path = self.write(root, "numeric.json", numeric)
            text_path = self.write(root, "text.json", text)
            compare_state_formats.main([
                "--numeric", str(numeric_path), "--text", str(text_path), "--out-dir", str(root / "out"),
            ])
            result = json.loads((root / "out" / "comparison.json").read_text(encoding="utf-8"))
            self.assertAlmostEqual(result["stage2"]["pairwise"]["hit@10"]["mean"], 0.2)
            self.assertEqual(result["stage2"]["pairwise"]["hit@10"]["wins"], 2)
            self.assertEqual(result["stage2"]["aggregate"]["text"]["hit@10"], 0.6)
            self.assertIn("文字 state − 数值 state", (root / "out" / "comparison.md").read_text(encoding="utf-8"))

    def test_different_date_sets_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            numeric_path = self.write(root, "numeric.json", comparison(0.4, 0.4, ("2026-10-01",)))
            text_path = self.write(root, "text.json", comparison(0.6, 0.6, ("2026-10-02",)))
            with self.assertRaisesRegex(compare_state_formats.ev.InputError, "different dates"):
                compare_state_formats.main([
                    "--numeric", str(numeric_path), "--text", str(text_path), "--out-dir", str(root / "out"),
                ])

    def test_missing_per_date_section_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            numeric = comparison(0.4, 0.4)
            text = comparison(0.6, 0.6)
            del text["stage2"]["per_date"]["2026-10-02"]
            numeric_path = self.write(root, "numeric.json", numeric)
            text_path = self.write(root, "text.json", text)
            with self.assertRaisesRegex(compare_state_formats.ev.InputError, "missing stage2 for 2026-10-02"):
                compare_state_formats.main([
                    "--numeric", str(numeric_path), "--text", str(text_path), "--out-dir", str(root / "out"),
                ])

    def test_missing_tree_and_jev_rankers_are_omitted(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            numeric = comparison(0.4, 0.4)
            text = comparison(0.6, 0.6)
            for document in (numeric, text):
                for row in document["stage2"]["per_date"].values():
                    del row["tree"]
                    del row["jev"]
            numeric_path = self.write(root, "numeric.json", numeric)
            text_path = self.write(root, "text.json", text)
            compare_state_formats.main([
                "--numeric", str(numeric_path), "--text", str(text_path), "--out-dir", str(root / "out"),
            ])
            markdown = (root / "out" / "comparison.md").read_text(encoding="utf-8")
            self.assertNotIn("local tree", markdown)
            self.assertNotIn("| JEV |", markdown)
            self.assertIn("DeepSeek text state", markdown)


if __name__ == "__main__":
    unittest.main()
