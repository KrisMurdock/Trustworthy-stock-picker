import pathlib
import tempfile
import unittest
from types import SimpleNamespace

import numpy as np

import limit_up_board_analysis as board_analysis
import limit_up_exits as exits
import limit_up_study as study
from test_limit_up_study import price_fixture, set_bar


class LimitUpBoardAnalysisTests(unittest.TestCase):
    def test_first_chase_depth_and_unknown_are_distinct(self):
        groups = board_analysis.board_groups(
            [True, True, True, True, False], [False, True, True, True, False],
            [False, True, True, False, True], [False, False, True, False, True])
        self.assertEqual(np.flatnonzero(groups['first_touch']).tolist(), [0])
        self.assertEqual(np.flatnonzero(groups['continuation_touch']).tolist(), [1, 2, 3])
        self.assertEqual(np.flatnonzero(groups['one_to_two']).tolist(), [1])
        self.assertEqual(np.flatnonzero(groups['two_or_more']).tolist(), [2])
        self.assertEqual(np.flatnonzero(groups['continuation_depth_unknown']).tolist(), [3])
        self.assertEqual(np.flatnonzero(groups['prior_unknown']).tolist(), [4])

    def test_unknown_prior_state_does_not_become_first_or_sealed(self):
        with self.assertRaisesRegex(ValueError, 'unknown board state'):
            board_analysis.board_groups([False], [True], [False], [False])
        with self.assertRaisesRegex(ValueError, 'inconsistent'):
            board_analysis.board_groups([True], [True, False], [True], [False])

    def test_normal_board_state_rejects_gap_and_does_not_use_later_prices(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 61, 10.0, 11.0, 10.0, 11.0)
        set_bar(raw, adjusted, 62, 11.0, 12.1, 11.0, 12.1)
        known, sealed = board_analysis.board_states(raw, adjusted, 10)
        self.assertTrue(known[61] and known[62])
        self.assertTrue(sealed[61] and sealed[62])
        raw[60, :] = 0
        known_missing, sealed_missing = board_analysis.board_states(raw, adjusted, 10)
        self.assertFalse(known_missing[61])
        self.assertFalse(sealed_missing[61])
        self.assertTrue(known_missing[62] and sealed_missing[62])
        set_bar(raw, adjusted, 63, 12.1, 13.31, 12.1, 12.1)
        self.assertEqual(board_analysis.board_states(raw, adjusted, 10)[1][61], sealed_missing[61])
        with self.assertRaisesRegex(ValueError, 'unsupported'):
            board_analysis.board_states(raw, adjusted, 5)

    def test_buy_day_and_following_seals_are_outcomes_not_entry_filters(self):
        groups = board_analysis.board_groups([True, True, True], [True, True, True],
                                             [True, True, True], [False, False, False])
        events = {'date': np.array(['2024-01-02', '2024-01-03', '2024-01-04']),
                  'closed_at_limit': np.array([False, True, True])}
        outcome = board_analysis.board_outcome(events, groups['continuation_touch'],
                                               [False, True, False], [False, True, False])
        self.assertEqual(outcome['touches'], 3)
        self.assertEqual(outcome['sealed_buy_day'], 2)
        self.assertEqual(outcome['sealed_both_days'], 1)
        self.assertEqual(outcome['next_day_unknown_after_seal'], 1)
        self.assertEqual(outcome['both_days_sealed_rate_known'], 0.5)
        events['closed_at_limit'][:] = False
        self.assertEqual(np.flatnonzero(groups['continuation_touch']).tolist(), [0, 1, 2])
        with self.assertRaisesRegex(ValueError, 'invalid'):
            board_analysis.board_outcome(events, [True] * 3, [False] * 3, [True, False, False])

    def test_holding_exit_sells_next_open_after_first_unsealed_close(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.2, 11.0, 10.2, 11.0)
        set_bar(raw, adjusted, 63, 11.2, 12.1, 11.2, 12.1)
        set_bar(raw, adjusted, 64, 12.4, 13.31, 12.4, 12.7)
        set_bar(raw, adjusted, 65, 12.7, 13.0, 12.5, 12.7)
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 10, view)
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(view, known, sealed, 62, 10)
        self.assertAlmostEqual(ratio, 12.7 / 11.0)
        self.assertEqual((delay, target, reason), (0, 3, 'break_close'))
        set_bar(raw, adjusted, 62, 10.2, 11.0, 10.2, 10.8)
        set_bar(raw, adjusted, 63, 10.8, 11.0, 10.7, 10.8)
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 10, view)
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(view, known, sealed, 62, 10)
        self.assertAlmostEqual(ratio, 10.8 / 11.0)
        self.assertEqual((delay, target, reason), (0, 1, 'break_close'))

    def test_holding_exit_caps_at_five_and_preserves_unknown_path(self):
        dates, raw, adjusted = price_fixture()
        previous_cents = 1000
        for index in range(62, 67):
            limit_cents = int(study.limit_cents(previous_cents, 10))
            set_bar(raw, adjusted, index, previous_cents / 100, limit_cents / 100,
                    previous_cents / 100, limit_cents / 100)
            previous_cents = limit_cents
        set_bar(raw, adjusted, 67, previous_cents / 100, previous_cents / 100,
                previous_cents / 100, previous_cents / 100)
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 10, view)
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(view, known, sealed, 62, 10)
        self.assertAlmostEqual(ratio, previous_cents / 1100)
        self.assertEqual((delay, target, reason), (0, 5, 'max_hold'))
        raw[63, :] = 0
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 10, view)
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(view, known, sealed, 62, 10)
        self.assertTrue(np.isnan(ratio))
        self.assertEqual((delay, target, reason), (-1, -1, 'board_unknown'))

    def test_holding_exit_delays_after_blocked_open(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.2, 11.0, 10.2, 10.8)
        set_bar(raw, adjusted, 63, 10.2, 10.5, 10.1, 10.3)
        set_bar(raw, adjusted, 64, 10.4, 10.5, 10.3, 10.4)
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 10, view)
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(view, known, sealed, 62, 10)
        self.assertAlmostEqual(ratio, 10.4 / 11.0)
        self.assertEqual((delay, target, reason), (1, 1, 'break_close'))
        ratio, delay, target, reason = board_analysis.hold_until_break_exit(
            view, known, sealed, 62, 10, sell_limit=10)
        self.assertAlmostEqual(ratio, 10.2 / 11.0)
        self.assertEqual((delay, target, reason), (0, 1, 'break_close'))
        with self.assertRaisesRegex(ValueError, 'invalid'):
            board_analysis.hold_until_break_exit(view, known, sealed, 62, 10, sell_limit=20)

    def test_posthoc_month_split_never_includes_other_signal_years(self):
        events = {
            'date': np.array(['2023-09-01', '2024-09-02', '2024-10-08', '2025-09-01']),
            'code': np.array(['sz300001', 'sz300002', 'sz300003', 'sz300004']),
            'closed_at_limit': np.array([False, False, False, False]),
            'sale_ratio': np.array([[1.1] * 4, [1.1] * 4, [0.9] * 4, [1.1] * 4]),
            'delay': np.zeros((4, 4), dtype=np.int16),
        }
        paths = {
            'previous_known': np.array([True] * 4), 'previous_sealed': np.array([False] * 4),
            'earlier_known': np.array([True] * 4), 'earlier_sealed': np.array([False] * 4),
            'next_known': np.array([True] * 4), 'next_sealed': np.array([False] * 4),
            'holding_ratio': np.array([1.1, 1.1, 0.9, 1.1]),
            'holding_delay': np.zeros(4, dtype=np.int16),
            'holding_board_ratio': np.array([1.1, 1.1, 0.9, 1.1]),
            'holding_board_delay': np.zeros(4, dtype=np.int16),
            'holding_reason': np.array(['break_close'] * 4),
        }
        first = board_analysis.summarize_cohort(events, np.ones(4, dtype=bool), paths)['first_touch']
        self.assertEqual(first['posthoc_september_2024']['september']['wins'], 1)
        self.assertEqual(first['posthoc_september_2024']['september']['closed'], 1)
        self.assertEqual(first['posthoc_september_2024']['other_months']['losses'], 1)
        self.assertEqual(first['posthoc_september_2024']['other_months']['closed'], 1)
        self.assertFalse(first['sample_ok'])
        self.assertFalse(first['profit_targets_passed'])

    def test_existing_result_is_rejected_before_reading_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'result.json'
            path.write_text('keep', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'refusing to overwrite'):
                board_analysis.run(SimpleNamespace(result=path))
            self.assertEqual(path.read_text(encoding='utf-8'), 'keep')

    def test_board_replay_refuses_holdout_signals_before_accessing_inputs(self):
        events = {'date': np.array(['2025-01-02'])}
        with self.assertRaisesRegex(ValueError, 'development signal dates'):
            board_analysis.replay_paths(SimpleNamespace(), events, np.array([]))


if __name__ == '__main__':
    unittest.main()
