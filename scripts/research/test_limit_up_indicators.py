import unittest

import numpy as np

import limit_up_indicators as indicators


def fixture():
    close = np.linspace(10.0, 30.0, 100)
    adjusted = np.column_stack((close, close + 0.1, close - 0.1, close, np.full(100, 1000)))
    return adjusted, np.ones(100, dtype=bool)


class IndicatorTests(unittest.TestCase):
    def test_ema_has_explicit_seed_and_weights(self):
        actual = indicators.ema([1, 2, 3, 4, 5], 3)
        np.testing.assert_allclose(actual[2:], [2, 3, 4])
        self.assertTrue(np.isnan(actual[1]))

    def test_previous_day_indicators_ignore_later_prices(self):
        adjusted, valid = fixture()
        before = indicators.calculate(adjusted, valid)
        adjusted[80:, :4] *= 5
        after = indicators.calculate(adjusted, valid)
        for name in ('macd', 'kdj_k', 'dmi_difference', 'atr_ratio', 'close_up'):
            np.testing.assert_equal(before[name][:80], after[name][:80])

    def test_missing_day_restarts_indicator_warmup(self):
        adjusted, valid = fixture()
        valid[60] = False
        values = indicators.calculate(adjusted, valid)
        self.assertTrue(np.isnan(values['macd'][80]))
        self.assertTrue(np.isfinite(values['kdj_k'][80]))
        self.assertTrue(np.isfinite(values['macd'][99]))

    def test_trend_and_atr_are_consistent(self):
        adjusted, valid = fixture()
        values = indicators.calculate(adjusted, valid)
        self.assertGreater(values['dmi_difference'][70], 0)
        self.assertGreater(values['atr_ratio'][70], 0)
        self.assertTrue(values['close_up'][70])
        self.assertGreaterEqual(values['kdj_k'][70], 0)
        self.assertLessEqual(values['kdj_k'][70], 100)

    def test_filter_intersections_do_not_admit_missing_indicators(self):
        features = {'macd': np.array([1., -1., np.nan]), 'kdj_k': np.array([75., 80., np.nan]),
                    'dmi_difference': np.array([1., -1., np.nan]),
                    'atr_ratio': np.array([0.03, 0.07, np.nan]),
                    'close_up': np.array([True, False, False])}
        masks = indicators.filter_masks(features, np.array([60., 70., np.nan]))
        self.assertEqual(len(masks), 11)
        np.testing.assert_array_equal(masks['macd_red_and_k_ge_70'], [True, False, False])
        np.testing.assert_array_equal(masks['pdi_gt_mdi_and_atr_2_to_6_pct'], [True, False, False])


if __name__ == '__main__':
    unittest.main()
