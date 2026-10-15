#!/usr/bin/env python3
"""Run the browser model with Node and compare financial outputs to the CLI."""
import json
from pathlib import Path
import random
import subprocess
import unittest
from decimal import Decimal

ROOT = Path(__file__).resolve().parents[1]
MODEL = ROOT / "internal/dashboard/static/strategy-model.js"
SCRIPT = ROOT / "scripts/calculate_strategy.sh"
BASE = dict(account="100000", cash="80000", budget="20000", max_loss="500",
            stop="45", slippage="0.10", sell_fee="20")
RUNNER = """
import {readFileSync} from 'node:fs';
const {calculateStrategy} = await import('data:text/javascript;base64,' + readFileSync(process.argv[1]).toString('base64'));
const cases = JSON.parse(readFileSync(0, 'utf8'));
console.log(JSON.stringify(cases.map(input => {
  try { return {result: calculateStrategy(input)}; }
  catch (error) { return {error: error.message, field: error.field}; }
})));
"""


def browser_model(cases):
    result = subprocess.run(["node", "--input-type=module", "-e", RUNNER, str(MODEL)],
                            input=json.dumps(cases), text=True, capture_output=True, timeout=10, check=True)
    return json.loads(result.stdout)


def cli_model(values):
    command = ["bash", str(SCRIPT), "--json"]
    for key, value in values.items():
        if key == "hold":
            for lot in value:
                command += ["--hold", ":".join([lot["price"], lot["shares"], lot.get("buy_fee", "0")])]
        else:
            command += ["--" + key.replace("_", "-"), value]
    result = subprocess.run(command, text=True, capture_output=True, timeout=5, check=True)
    return json.loads(result.stdout)


class StrategyModelTest(unittest.TestCase):
    def assert_equivalent(self, browser, cli, key=""):
        if isinstance(browser, dict):
            for name, value in browser.items():
                self.assertIn(name, cli)
                self.assert_equivalent(value, cli[name], name)
        elif isinstance(browser, list):
            self.assertEqual(len(browser), len(cli), key)
            for x, y in zip(browser, cli):
                self.assert_equivalent(x, y, key)
        elif isinstance(browser, (bool, type(None))):
            self.assertEqual(browser, cli, key)
        else:
            try:
                a, b = Decimal(browser), Decimal(cli)
            except Exception:
                self.assertEqual(browser, cli, key)
                return
            # Only non-terminating ratios/averages may differ in display precision.
            if "pct" in key or key == "average_price":
                self.assertLess(abs(a - b), Decimal("1e-30"), key)
            else:
                self.assertEqual(a, b, key)

    def test_cli_parity_across_risk_cash_fee_tick_and_lot_boundaries(self):
        cases = [
            dict(BASE, hold=[dict(price="47.05", shares="215", buy_fee="10")], add_price="46"),
            dict(BASE, entry="47.05", buy_fee="10", initial_risk_pct="100"),
            dict(BASE, entry="47.05", buy_fee="10", add_price="46"),
            dict(BASE, entry="47.05", buy_fee="10", add_price="46", max_loss="1000", risk_pct="1"),
            dict(BASE, cash="9419.99", entry="47.05", buy_fee="10", initial_risk_pct="100"),
            dict(BASE, cash="0", entry="47.05"),
            dict(BASE, entry="47.05", buy_fee="500"),
            dict(BASE, hold=[dict(price="47", shares="100", buy_fee="5"), dict(price="46", shares="100", buy_fee="5")], target_profit="500"),
            dict(BASE, hold=[dict(price="40", shares="200", buy_fee="5"), dict(price="47.05", shares="215", buy_fee="5")], add_price="46"),
            dict(BASE, hold=[dict(price="47.05", shares="400", buy_fee="10")], add_price="46"),
            dict(BASE, budget="1000", hold=[dict(price="47.05", shares="215", buy_fee="10")], add_price="46"),
            dict(BASE, entry="10", stop="9.9", slippage="0", budget="2199.99", min_shares="100", lot_step="100", initial_risk_pct="100"),
            dict(BASE, entry="0.1", stop="0.05", slippage="0", cash="0.3", budget="0.3", sell_fee="0", min_shares="1"),
            dict(BASE, entry="47.055", stop="45.005", tick="0.005", min_shares="1", target_pct="2.55"),
            dict(BASE, hold=[dict(price="1", shares="1", buy_fee="0")], stop="0.5", sell_fee="0", slippage="0.1"),
        ]
        generator = random.Random(215)
        for _ in range(40):
            cases.append(dict(BASE, entry="47.05", add_price="46", min_shares="1",
                              cash=str(generator.randrange(1, 80000)),
                              budget=str(generator.randrange(1, 80000)),
                              max_loss=str(generator.randrange(1, 2000)),
                              buy_fee=str(generator.randrange(0, 50)),
                              initial_risk_pct=str(generator.randrange(1, 101))))
        for case, actual in zip(cases, browser_model(cases)):
            with self.subTest(case=case):
                self.assertNotIn("error", actual)
                expected = cli_model(case)
                # UI wording and metadata differ, financial decisions must agree.
                actual["result"].pop("warnings")
                self.assert_equivalent(actual["result"], expected)

    def test_invalid_inputs_never_produce_results(self):
        changes = [dict(account="NaN"), dict(cash="Infinity"), dict(cash="-1"), dict(cash="1e3"),
                   dict(cash="100001"), dict(budget="100001"), dict(stop="0"), dict(stop="47.05"),
                   dict(risk_pct="0"), dict(risk_pct="101"), dict(slippage="45"), dict(tick="0"),
                   dict(entry="47.051"), dict(min_shares="201", lot_step="100"), dict(target_profit="0"),
                   dict(target_pct="3", target_profit="500"), dict(account=""), dict(account=100000),
                   dict(hold=[dict(price="47", shares="2.5")]), dict(hold=[dict(price="47", shares="0")]),
                   dict(initial_risk_pct="101"), dict(buy_fee="0.000000001")]
        for case, result in zip(changes, browser_model([dict(BASE, entry="47.05", **change) if "entry" not in change else dict(BASE, **change) for change in changes])):
            with self.subTest(case=case):
                self.assertIn("error", result)
                self.assertTrue(result["field"])


if __name__ == "__main__":
    unittest.main()
