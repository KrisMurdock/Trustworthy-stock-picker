import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

PATH = Path(__file__).with_name("compile_candidates.py")
spec = importlib.util.spec_from_file_location("compile_candidates", PATH)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CompileTests(unittest.TestCase):
    def test_compiles_subset_without_mutating_baseline(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            base = {"schema_version": 3, "signals": [{"code": "sh600000", "date": "2026-01-02", "atr": .04}, {"code": "sz000001", "date": "2026-01-02", "atr": .05}], "bars": {}}
            features = [{"code": "sh600000", "date": "2026-01-02", "trend": True}, {"code": "sz000001", "date": "2026-01-02", "trend": False}]
            values = {"base.json": base, "features.json": features, "constraints.json": {"max_drawdown": .3, "absolute_tail_loss": 10000, "min_closed_trades": 1, "min_signal_days": 1, "min_profit_factor": 1, "min_net_expectancy": 0, "stress_slippage": [.002]}, "inference.json": {"block_sessions": 20, "min_independent_blocks": 4, "bootstrap_samples": 100, "confidence": .95, "seed": 1}, "budget.json": {"max_candidates": 1, "max_attempts_per_candidate": 1, "max_review_calls": 0, "timeout": "10m"}}
            for name, value in values.items():
                (root / name).write_text(json.dumps(value))
            (root / "contract.md").write_text("fixed")
            args = type("Args", (), {"base_input": root / "base.json", "features": root / "features.json", "constraints": root / "constraints.json", "inference": root / "inference.json", "budget": root / "budget.json", "human_contract": root / "contract.md", "run_id": "r1", "candidate": ["trend=trend"]})
            result = module.compile_request(args)
            self.assertEqual(len(result["inputs"]["baseline"]["signals"]), 2)
            self.assertEqual([x["code"] for x in result["inputs"]["trend"]["signals"]], ["sh600000"])

    def test_rejects_invalid_candidate_name(self):
        with self.assertRaises(ValueError):
            module.parse_candidate("Bad Name=x")


if __name__ == "__main__":
    unittest.main()
