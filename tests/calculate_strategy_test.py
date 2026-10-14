#!/usr/bin/env python3
"""通过真实脚本入口验证金额边界、买入限制与交互行为；不访问行情或账户。"""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import unittest
from decimal import Decimal as D


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "calculate_strategy.sh"
BASE = ["--account", "100000", "--cash", "80000", "--budget", "20000",
        "--max-loss", "500", "--stop", "45", "--slippage", "0.10",
        "--sell-fee", "20"]


class StrategyTest(unittest.TestCase):
    def run_script(self, *args, expected=0, stdin="", env=None):
        result = subprocess.run(["/bin/bash", str(SCRIPT), *args], input=stdin,
                                text=True, capture_output=True, timeout=5, env=env)
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        return result

    def calculate(self, *args):
        return json.loads(self.run_script(*BASE, *args, "--json").stdout)

    def test_existing_215_shares_leaves_no_legal_addition(self):
        result = self.calculate("--hold", "47.05:215:10", "--add-price", "46")
        position = result["current_position"]
        self.assertEqual(D(position["risk_used"]), D("492.25"))
        self.assertEqual(D(position["risk_remaining"]), D("7.75"))
        self.assertEqual(result["orders"][0]["limits_shares"]["risk"], 7)
        self.assertEqual(result["orders"][0]["shares"], 0)
        self.assertEqual(D(position["target_price"]), D("48.61"))
        self.assertEqual(D(position["loss_budget_execution_price"]), D("44.87"))
        self.assertEqual(D(position["loss_budget_trigger_price"]), D("44.97"))
        self.assertEqual(D(result["cash_remaining"]), D("80000"))

    def test_full_risk_allocation_never_rounds_up(self):
        result = self.calculate("--entry", "47.05", "--initial-risk-pct", "100", "--buy-fee", "10")
        order = result["orders"][0]
        self.assertEqual(order["shares"], 218)
        self.assertEqual(D(order["position_after"]["risk_used"]), D("498.70"))
        self.assertEqual(D(order["cash_required"]), D("10266.90"))

    def test_minimum_order_does_not_expand_risk(self):
        result = self.calculate("--entry", "47.05", "--buy-fee", "10", "--add-price", "46")
        self.assertEqual(result["orders"][0]["shares"], 0)
        self.assertEqual(result["orders"][1]["blocked_by"], ["no_initial_position"])
        self.assertIsNone(result["final_position"])
        self.assertEqual(D(result["cash_remaining"]), D("80000"))

    def test_initial_then_add_respects_total_budget_and_fees(self):
        result = self.calculate("--max-loss", "1000", "--risk-pct", "1",
                                "--entry", "47.05", "--add-price", "46", "--buy-fee", "10")
        self.assertEqual([item["shares"] for item in result["orders"]], [223, 206])
        final = result["final_position"]
        self.assertEqual(D(final["invested"]), D("19968.15"))
        self.assertEqual(D(final["buy_fees"]), D("20"))
        self.assertEqual(D(final["risk_used"]), D("746.05"))
        self.assertEqual(D(result["cash_remaining"]), D("60011.85"))

    def test_cash_is_net_of_new_fee(self):
        result = self.calculate("--cash", "9419.99", "--entry", "47.05",
                                "--initial-risk-pct", "100", "--buy-fee", "10")
        self.assertEqual(result["orders"][0]["limits_shares"]["cash"], 199)
        self.assertEqual(result["orders"][0]["shares"], 0)

    def test_main_board_rounding_and_exact_decimal_boundary(self):
        result = self.calculate("--cash", "10000", "--entry", "10", "--stop", "9.90",
                                "--slippage", "0", "--budget", "2199.99", "--min-shares", "100",
                                "--lot-step", "100", "--initial-risk-pct", "100")
        self.assertEqual(result["orders"][0]["shares"], 200)
        exact = self.calculate("--cash", "0.3", "--budget", "0.3", "--entry", "0.1",
                               "--stop", "0.05", "--slippage", "0", "--min-shares", "1",
                               "--sell-fee", "0", "--initial-risk-pct", "100")
        self.assertEqual(exact["orders"][0]["shares"], 3)
        self.assertEqual(D(exact["cash_remaining"]), 0)

    def test_multiple_lots_and_fixed_profit(self):
        result = self.calculate("--hold", "47:100:5", "--hold", "46:100:5", "--target-profit", "500")
        final = result["final_position"]
        self.assertEqual(final["shares"], 200)
        self.assertEqual(D(final["average_price"]), D("46.5"))
        self.assertEqual(D(final["target_price"]), D("49.15"))
        self.assertEqual(D(final["net_profit_at_target"]), D("500"))

    def test_existing_over_budget_and_profits_do_not_fund_risk(self):
        result = self.calculate("--hold", "47.05:400:10", "--add-price", "46")
        self.assertTrue(result["current_position"]["risk_exceeded"])
        self.assertEqual(result["orders"][0]["shares"], 0)
        result = self.calculate("--hold", "40:200:5", "--hold", "47.05:215:5", "--add-price", "46")
        self.assertEqual(D(result["current_position"]["risk_used"]), D("492.25"))
        self.assertEqual(result["orders"][0]["shares"], 0)
        result = self.calculate("--budget", "1000", "--hold", "47.05:215:10", "--add-price", "46")
        self.assertTrue(result["current_position"]["budget_exceeded"])
        self.assertEqual(result["orders"][0]["shares"], 0)

    def test_fee_only_or_zero_cash_blocks_order(self):
        for args in (("--buy-fee", "500"), ("--cash", "0")):
            result = self.calculate("--entry", "47.05", *args)
            self.assertEqual(result["orders"][0]["shares"], 0)
            self.assertGreaterEqual(D(result["cash_remaining"]), 0)

    def test_risk_budget_uses_stricter_of_amount_and_account_fraction(self):
        for maximum, expected in (("800", "500"), ("100", "100")):
            result = self.calculate("--max-loss", maximum, "--entry", "47.05")
            self.assertEqual(D(result["risk_budget"]), D(expected))

    def test_invalid_and_missing_inputs(self):
        self.run_script(expected=2)
        bad = [
            ("--account", "NaN"), ("--cash", "Infinity"), ("--cash", "-1"),
            ("--cash", "100001"), ("--budget", "100001"), ("--stop", "0"),
            ("--stop", "47.05"), ("--risk-pct", "0"), ("--risk-pct", "101"),
            ("--slippage", "45"), ("--tick", "0"), ("--entry", "47.051"),
            ("--min-shares", "201", "--lot-step", "100"), ("--hold", "47:2.5"),
            ("--hold", "47:0"), ("--hold", "47:215"), ("--target-profit", "0"),
            ("--target-pct", "3", "--target-profit", "500"), ("--unknown", "1"),
            ("--cash", "1e6"), ("--cash", "1000000000001"),
        ]
        for args in bad:
            with self.subTest(args=args):
                self.run_script(*BASE, "--entry", "47.05", *args, expected=2)

    def test_interactive_input_and_eof(self):
        answers = "\n".join(["100000", "80000", "20000", "500", "0.5", "45", "0.1",
                             "47.05:215:10", "46", "0", "20", "3", "200", "1"]) + "\n"
        result = self.run_script("--interactive", "--json", stdin=answers)
        self.assertEqual(json.loads(result.stdout)["orders"][0]["shares"], 0)
        self.run_script("--interactive", expected=2)

    def test_cancel_interactive(self):
        process = subprocess.Popen(["/bin/bash", str(SCRIPT), "--interactive"],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertIn("金额单位".encode(), process.stderr.readline())
            process.send_signal(signal.SIGINT)
            _, stderr = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 130)
            self.assertNotIn(b"Traceback", stderr)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()

    def test_missing_python_and_read_only_invocation(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(os.environ, PATH=directory)
            self.run_script("--help", expected=127, env=env)
            copy = Path(directory) / "calculate_strategy.sh"
            shutil.copyfile(SCRIPT, copy)
            copy.chmod(0o444)
            result = subprocess.run(["/bin/bash", str(copy), "--help"], capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(sorted(os.listdir(directory)), ["calculate_strategy.sh"])

    def test_text_output_contains_actionable_state(self):
        result = self.run_script(*BASE, "--hold", "47.05:215:10", "--add-price", "46")
        for expected in ("允许买入：0 股", "风险额度", "492.25", "48.61", "资金止损边界"):
            self.assertIn(expected, result.stdout)


if __name__ == "__main__":
    unittest.main()
