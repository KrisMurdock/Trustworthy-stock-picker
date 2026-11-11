import unittest

import numpy as np

import limit_up_dynamic_exits as dynamic
import limit_up_study as study


class DynamicExitTests(unittest.TestCase):
    def test_weak_thresholds_use_only_the_next_close(self):
        ratios = np.array([0.96, 0.97, 0.99, 1.0, 1.03, np.nan])
        self.assertEqual(dynamic.choose_holds(ratios, dynamic.POLICIES[0]).tolist(), [2, 3, 3, 3, 3, 3])
        self.assertEqual(dynamic.choose_holds(ratios, dynamic.POLICIES[1]).tolist(), [2, 2, 2, 3, 3, 3])
        self.assertEqual(dynamic.choose_holds(ratios, dynamic.POLICIES[2]).tolist(), [2, 2, 2, 2, 3, 3])

    def test_strong_close_extension_and_missing_close_fallback(self):
        ratios = np.array([0.96, 0.97, 1.07, 1.08, 1.10, np.nan, np.inf, 0])
        holds = dynamic.choose_holds(ratios, dynamic.POLICIES[3])
        self.assertEqual(holds.tolist(), [2, 3, 3, 5, 5, 3, 3, 3])

    def test_next_close_does_not_use_later_sessions(self):
        view = {'normal': np.array([True, True, True])}
        raw = np.full((3, 5), 100_000_000, dtype=np.int64)
        raw[1, 3] = 97_000_000
        self.assertEqual(dynamic.next_close_ratio(view, raw, 0, 10_000), 0.97)
        raw[2, 3] = 1_000_000
        self.assertEqual(dynamic.next_close_ratio(view, raw, 0, 10_000), 0.97)
        view['normal'][1] = False
        self.assertTrue(np.isnan(dynamic.next_close_ratio(view, raw, 0, 10_000)))

    def test_exact_entry_price_is_not_classified_below_threshold(self):
        view = {'normal': np.array([True, True])}
        raw = np.full((2, 5), 17_590_000, dtype=np.int64)
        ratio = dynamic.next_close_ratio(view, raw, 0, 1759)
        self.assertEqual(ratio, 1.0)
        self.assertEqual(dynamic.choose_holds([ratio], dynamic.POLICIES[1]).tolist(), [3])
        raw[1, 3] -= 1
        self.assertTrue(np.isnan(dynamic.next_close_ratio(view, raw, 0, 1759)))

    def test_rebound_candle_guards_use_only_next_session(self):
        view = {'normal': np.array([True, True, True])}
        raw = np.full((3, 5), 100_000_000, dtype=np.int64)
        raw[1, :4] = [100_000_000, 110_000_000, 90_000_000, 95_000_000]
        self.assertEqual(dynamic.next_candle_conditions(view, raw, 0), (True, True))
        raw[2, :4] = [200_000_000, 220_000_000, 180_000_000, 210_000_000]
        self.assertEqual(dynamic.next_candle_conditions(view, raw, 0), (True, True))
        raw[1, 3] = 100_000_000
        self.assertEqual(dynamic.next_candle_conditions(view, raw, 0), (False, False))
        view['normal'][1] = False
        self.assertEqual(dynamic.next_candle_conditions(view, raw, 0), (False, False))

    def test_rebound_policies_require_a_known_weak_close_and_the_guard(self):
        ratios = np.array([0.98, 0.98, 0.98, 1.0, np.nan])
        guards = {'below_open': np.array([True, False, True, True, True]),
                  'below_midpoint': np.array([False, True, True, True, True])}
        self.assertEqual(dynamic.choose_holds(ratios, dynamic.REBOUND_POLICIES[0], guards).tolist(), [2, 3, 2, 3, 3])
        self.assertEqual(dynamic.choose_holds(ratios, dynamic.REBOUND_POLICIES[1], guards).tolist(), [3, 2, 2, 3, 3])
        with self.assertRaisesRegex(ValueError, 'missing next-candle guard'):
            dynamic.choose_holds(ratios, dynamic.REBOUND_POLICIES[0])
        with self.assertRaisesRegex(ValueError, 'invalid next-candle guard'):
            dynamic.choose_holds(ratios, dynamic.REBOUND_POLICIES[0], {'below_open': [1, 0, 1, 0, 1]})

    def test_exit_choice_preserves_delays_and_does_not_discard_unresolved(self):
        ratios = np.array([[1.0, 1.1, 1.2, 1.3], [1.0, np.nan, 1.2, 1.3], [1.0, 1.1, 1.2, 1.3]])
        events = {'date': np.array(['2021-01-01', '2021-01-02', '2021-01-03']),
                  'sale_ratio': ratios, 'delay': np.array([[0, 1, 0, 2], [0, -1, 0, 0], [0, 0, 0, 3]])}
        replay = dynamic.apply_holds(events, np.array([2, 2, 5]))
        column = study.HOLDS.index(3)
        self.assertAlmostEqual(replay['sale_ratio'][0, column], 1.1)
        self.assertTrue(np.isnan(replay['sale_ratio'][1, column]))
        self.assertAlmostEqual(replay['sale_ratio'][2, column], 1.3)
        self.assertEqual(replay['delay'][:, column].tolist(), [1, -1, 3])
        self.assertAlmostEqual(events['sale_ratio'][0, column], 1.2)
        with self.assertRaisesRegex(ValueError, 'hold choices'):
            dynamic.apply_holds(events, np.array([0, 2, 5]))

    def test_2025_and_later_signal_returns_are_excluded(self):
        events = {'date': np.array(['2020-12-31', '2021-01-04', '2024-12-31', '2025-01-02']),
                  'board': np.array([20, 20, 20, 20]),
                  'ma20_above_ma60': np.array([True, True, True, True]),
                  'gap': np.array([0.04, 0.04, 0.04, 0.04])}
        self.assertEqual(dynamic.development_indices(events).tolist(), [1, 2])

    def test_changed_exit_diagnostics_keep_unresolved_comparisons(self):
        ratios = np.array([[1.0, 1.1, 0.9, 1.0], [1.0, 0.9, 1.1, 1.0],
                           [1.0, np.nan, 0.9, 1.0], [1.0, 1.0, 1.0, 1.0]])
        events = {'date': np.array(['2021-01-01'] * 4), 'sale_ratio': ratios,
                  'delay': np.zeros_like(ratios)}
        holds = np.array([2, 2, 2, 3])
        replay = dynamic.apply_holds(events, holds)
        result = dynamic.compare_with_fixed(events, replay, holds, np.ones(4, dtype=bool))
        self.assertEqual(result['changed'], 3)
        self.assertEqual(result['comparable'], 2)
        self.assertEqual(result['unresolved_comparisons'], 1)
        self.assertEqual((result['improved'], result['worsened']), (1, 1))
        self.assertEqual((result['loss_to_gain'], result['gain_to_loss']), (1, 1))
        self.assertAlmostEqual(result['mean_return_change'], 0)


if __name__ == '__main__':
    unittest.main()
