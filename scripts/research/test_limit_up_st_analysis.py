import json
import pathlib
import tempfile
import unittest

import numpy as np

import limit_up_st_analysis as analysis
import limit_up_study as study


class HistoricalSTAnalysisTests(unittest.TestCase):
    def setUp(self):
        self.records = [
            {'code': 'sz300001', 'date': '2021-01-04', 'daily_cents': [10, 12, 9, 11]},
            {'code': 'sh688002', 'date': '2024-12-31', 'daily_cents': [20, 24, 19, 23]},
        ]
        self.candidates = analysis.event_records(self.records)
        self.keys = list(self.candidates)
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = pathlib.Path(self.directory.name) / 'statuses.jsonl'

    def write_rows(self, rows):
        self.path.write_text(''.join(json.dumps(row) + '\n' for row in rows), encoding='utf-8')

    def read_rows(self):
        return analysis.read_statuses([self.path], self.candidates, 'candidate-hash', {self.keys[0]}, 'sample-hash')

    def test_candidate_keys_prices_and_development_dates_are_validated(self):
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            analysis.event_records(self.records + [self.records[0]])
        for invalid in ('2025-01-01', '2021-02-30', '2021-1-04'):
            record = {**self.records[0], 'date': invalid}
            with self.subTest(date=invalid), self.assertRaises(ValueError):
                analysis.event_records([record])
        for prices in ([1, 2, 3], [1, 2, 0, 3], [1, 2, True, 3]):
            with self.subTest(prices=prices), self.assertRaisesRegex(ValueError, 'prices'):
                analysis.event_records([{**self.records[0], 'daily_cents': prices}])

    def test_unknown_status_does_not_default_to_non_st_and_retry_can_resolve_it(self):
        first = {'code': self.keys[0][0], 'date': self.keys[0][1], 'sample_sha256': 'sample-hash'}
        second = {'code': self.keys[1][0], 'date': self.keys[1][1], 'candidate_sha256': 'candidate-hash'}
        self.write_rows([{**first, 'status': 'ok', 'is_st': False}, {**second, 'status': 'unknown'}])
        statuses, unknown = self.read_rows()
        self.assertEqual(statuses, {self.keys[0]: False})
        self.assertEqual(unknown, {self.keys[1]})
        self.write_rows([{**second, 'status': 'unknown'}, {**second, 'status': 'ok', 'is_st': True}])
        statuses, unknown = self.read_rows()
        self.assertEqual(statuses, {self.keys[1]: True})
        self.assertEqual(unknown, set())

    def test_status_provenance_and_duplicate_success_are_enforced(self):
        row = {'code': self.keys[0][0], 'date': self.keys[0][1],
               'candidate_sha256': 'candidate-hash', 'status': 'ok', 'is_st': False}
        invalid_rows = (
            [{**row, 'candidate_sha256': 'wrong'}],
            [{**row, 'sample_sha256': 'sample-hash'}],
            [{**row, 'code': 'sz300999'}],
            [{**row, 'is_st': '0'}],
            [{**row, 'status': 'unknown'}],
            [{**row, 'status': 'unrecognized'}],
            [row, row],
            [{'code': self.keys[1][0], 'date': self.keys[1][1], 'sample_sha256': 'sample-hash',
              'status': 'ok', 'is_st': False}],
        )
        for rows in invalid_rows:
            self.write_rows(rows)
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                self.read_rows()

    def test_previous_session_features_ignore_signal_and_future_days(self):
        indicator = {name: np.array([1.0, 2.0, 999.0])
                     for name in ('dmi_difference', 'macd', 'kdj_k', 'atr_ratio')}
        volume = {'volume_ratio': np.array([3.0, 4.0, 999.0]),
                  'close_known': np.array([True, False, True]),
                  'close_up': np.array([True, False, True])}
        prior = analysis.features_before_signal(2, indicator, volume)
        self.assertEqual(prior['dmi_difference'], 2.0)
        self.assertEqual(prior['volume_ratio'], 4.0)
        self.assertFalse(prior['close_known'])
        for values in indicator.values():
            values[2] = -999.0
        self.assertEqual(analysis.features_before_signal(2, indicator, volume), prior)
        with self.assertRaisesRegex(ValueError, 'previous'):
            analysis.features_before_signal(0, indicator, volume)

    def test_prior_limit_relation_uses_unadjusted_exact_cents(self):
        self.assertEqual(analysis.prior_limit_relation(119, 100, False), -1)
        self.assertEqual(analysis.prior_limit_relation(120, 100, True), 0)
        self.assertEqual(analysis.prior_limit_relation(121, 100, False), 1)
        with self.assertRaisesRegex(ValueError, 'classification'):
            analysis.prior_limit_relation(120, 100, False)
        with self.assertRaisesRegex(ValueError, 'reference'):
            analysis.prior_limit_relation(120, 0, True)

    def test_missing_features_do_not_pass_conditions(self):
        events = {'date': np.array(['2021-01-04'] * 3),
                  'prior_reference_valid': np.array([True, False, True]),
                  'prior_limit': np.array([False, False, True]),
                  'above_ma20': np.array([True, False, True]),
                  'rsi6': np.array([50.0, np.nan, 49.0])}
        features = {'volume_ratio': np.array([1.1, np.nan, 1.0]),
                    'macd': np.array([0.1, np.nan, -0.1]),
                    'kdj_k': np.array([70.0, np.nan, 69.0]),
                    'atr_ratio': np.array([0.02, np.nan, 0.06]),
                    'close_known': np.array([True, False, True]),
                    'close_up': np.array([True, True, False])}
        masks = analysis.filter_masks(events, features)
        self.assertEqual(masks['prior_close_not_limit'][0].tolist(), [True, False, False])
        self.assertEqual(masks['prior_close_not_limit'][1].tolist(), [True, False, True])
        self.assertEqual(masks['prior_volume_gt_ma5'][0].tolist(), [True, False, False])
        self.assertEqual(masks['prior_volume_le_ma5'][0].tolist(), [False, False, True])
        self.assertEqual(masks['prior_atr_2_to_6_pct'][0].tolist(), [True, False, False])
        for name in ('prior_macd_red', 'prior_rsi_ge_50', 'prior_k_ge_70', 'prior_close_up'):
            self.assertEqual(masks[name][0].tolist(), [True, False, False])
            self.assertEqual(masks[name][1].tolist(), [True, False, True])

    def test_small_sample_cannot_pass_and_grid_stays_fixed(self):
        events = {'code': np.array(['sz300001', 'sh688002']),
                  'date': np.array(['2021-01-04', '2024-12-31']),
                  'sale_ratio': np.array([[1.2] * len(study.HOLDS), [0.9] * len(study.HOLDS)]),
                  'delay': np.zeros((2, len(study.HOLDS)), dtype=np.int16),
                  'closed_at_limit': np.array([True, False])}
        masks = {name: (np.ones(2, dtype=bool), np.ones(2, dtype=bool)) for name in analysis.FILTER_NAMES}
        results = analysis.evaluate_grid({'fixed_t3': events, 'weak_zero_t2_else_t3': events}, masks,
                                         np.array([True, True]), analysis.period_masks(events['date']))
        self.assertEqual(len(results), 20)
        self.assertTrue(all(not record['sample_ok'] and not record['targets_passed'] for record in results))
        self.assertEqual(results[0]['periods']['training_2021_2023']['20bp']['signals'], 1)

    def test_signal_close_exit_thresholds_use_exact_cents_and_no_future_day(self):
        close = np.array([10000, 9999, 9700, 9699, 9400, 8999, 0])
        entry = np.array([10000, 10000, 10000, 10000, 10000, 10000, 0])
        dynamic = np.array([2, 2, 3, 3, 2, 3, 2])
        holds = analysis.closing_exit_holds(close, entry, dynamic)
        self.assertEqual(tuple(holds), analysis.EXIT_NAMES)
        self.assertEqual(holds['broken_t1_else_t3'].tolist(), [3, 1, 1, 1, 1, 1, 3])
        self.assertEqual(holds['below_97_t1_else_t3'].tolist(), [3, 3, 3, 1, 1, 1, 3])
        self.assertEqual(holds['below_94_t1_else_t3'].tolist(), [3, 3, 3, 3, 3, 1, 3])
        self.assertEqual(holds['below_90_t1_else_t3'].tolist(), [3, 3, 3, 3, 3, 1, 3])
        self.assertEqual(holds['broken_t1_else_weak_zero'].tolist(), [2, 1, 1, 1, 1, 1, 2])
        with self.assertRaisesRegex(ValueError, 'price'):
            analysis.closing_exit_holds([10001], [10000], [3])
        with self.assertRaisesRegex(ValueError, 'price'):
            analysis.closing_exit_holds([1], [0], [3])

    def test_closing_exit_small_samples_cannot_pass(self):
        events = {'code': np.array(['sz300001', 'sh688002']),
                  'date': np.array(['2021-01-04', '2024-12-31']),
                  'sale_ratio': np.array([[1.2] * len(study.HOLDS), [0.9] * len(study.HOLDS)]),
                  'delay': np.zeros((2, len(study.HOLDS)), dtype=np.int16),
                  'closed_at_limit': np.array([True, False])}
        policies = analysis.closing_exit_holds([100, 90], [100, 100], [3, 2])
        results = analysis.evaluate_closing_exits(events, policies, np.array([True, True]),
                                                  analysis.period_masks(events['date']))
        self.assertEqual(len(results), 8)
        self.assertTrue(all(not record['sample_ok'] and not record['targets_passed'] for record in results))
        broken = next(record for record in results if record['policy'] == 'broken_t1_else_t3')
        self.assertEqual(broken['decisions']['selection_2024']['t1'], 1)
        self.assertEqual(broken['decisions']['training_2021_2023']['t3'], 1)

    def test_locked_candidate_diagnostics_split_losses_and_september_without_future_signals(self):
        events = {
            'code': np.array(['sz300001', 'sz300002', 'sh688003', 'sz300004', 'sz300005']),
            'date': np.array(['2021-01-04', '2021-01-05', '2024-09-30', '2024-10-08', '2025-01-02']),
            'sale_ratio': np.array([[0.89] * 4, [1.2] * 4, [1.1] * 4, [0.94] * 4, [99.0] * 4]),
            'delay': np.zeros((5, 4), dtype=np.int16),
            'closed_at_limit': np.array([False, True, True, False, True]),
        }
        candidate = np.array([True, True, True, True, False])
        holds = np.array([2, 3, 3, 2, 3])
        result = analysis.locked_candidate_diagnostics(events, candidate, holds)
        groups = result['summaries_20bp']
        self.assertEqual(groups['all']['training_2021_2023']['signals'], 2)
        self.assertEqual(groups['sealed']['training_2021_2023']['signals'], 1)
        self.assertEqual(groups['broken']['training_2021_2023']['signals'], 1)
        self.assertEqual(groups['planned_t2']['selection_2024']['signals'], 1)
        self.assertEqual(groups['planned_t3']['selection_2024']['signals'], 1)
        self.assertEqual(groups['september_2024']['selection_2024']['signals'], 1)
        self.assertEqual(groups['excluding_september_2024']['selection_2024']['signals'], 1)
        self.assertEqual(result['risk_20bp']['training_2021_2023']['loss_at_least_5pct'], 1)
        self.assertEqual(result['risk_20bp']['training_2021_2023']['loss_at_least_10pct'], 1)
        self.assertEqual(result['risk_20bp']['selection_2024']['loss_at_least_5pct'], 1)
        self.assertEqual(result['risk_20bp']['selection_2024']['loss_at_least_10pct'], 0)
        events['sale_ratio'][-1] = 0.01
        self.assertEqual(analysis.locked_candidate_diagnostics(events, candidate, holds), result)
        candidate[-1] = True
        with self.assertRaisesRegex(ValueError, 'holdout'):
            analysis.locked_candidate_diagnostics(events, candidate, holds)

    def test_locked_candidate_diagnostics_require_consistent_shapes_and_exit_holds(self):
        events = {'date': np.array(['2021-01-04']), 'closed_at_limit': np.array([True]),
                  'code': np.array(['sz300001']), 'sale_ratio': np.ones((1, 4)),
                  'delay': np.zeros((1, 4), dtype=np.int16)}
        with self.assertRaisesRegex(ValueError, 'inputs'):
            analysis.locked_candidate_diagnostics(events, [True, False], [3])
        with self.assertRaisesRegex(ValueError, 'inputs'):
            analysis.locked_candidate_diagnostics(events, [True], [1])


if __name__ == '__main__':
    unittest.main()
