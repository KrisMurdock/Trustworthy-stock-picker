import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace

import numpy as np

import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_st_analysis as analysis
import limit_up_study as study
import limit_up_volume as volume
import limit_up_win_analysis as win_analysis


class LimitUpWinAnalysisTests(unittest.TestCase):
    def test_only_development_dates_and_locked_board_universes_are_selected(self):
        events = {
            'date': np.array(['2021-01-04', '2024-12-31', '2025-01-01', '2020-12-31',
                              '2023-01-04', '2022-01-04', '2022-01-05', '2022-01-06']),
            'board': np.array([10, 10, 10, 10, 20, 20, 10, 10]),
            'ma20_above_ma60': np.array([True, True, True, True, True, True, False, True]),
            'gap': np.array([0.0, 0.069, 0.01, 0.01, 0.04, 0.02, 0.02, 0.07]),
        }
        self.assertEqual(win_analysis.development_indices(events).tolist(), [0, 1, 4])

    def test_filters_use_only_prior_features_and_exclude_unknown_values(self):
        signals = {
            'date': np.array(['2021-01-04'] * 4),
            'prior_reference_valid': np.array([True, True, False, True]),
            'prior_limit': np.array([False, True, False, False]),
            'above_ma20': np.array([True, False, True, True]),
            'rsi6': np.array([49.0, 60.0, 70.0, 80.0]),
            'closed_at_limit': np.array([True, True, False, False]),
            'sale_ratio': np.array([1.2, 0.8, 1.3, 0.7]),
        }
        features = {
            'volume_ratio': np.array([1.1, 1.0, np.nan, 0.8]),
            'macd': np.array([1.0, -1.0, 0.0, np.nan]),
            'kdj_k': np.array([49.0, 50.0, 70.0, np.nan]),
            'atr_ratio': np.array([0.019, 0.02, 0.059, 0.06]),
            'close_known': np.array([True, True, False, True]),
            'close_up': np.array([True, False, True, True]),
            'dmi_difference': np.array([0.1, 0.0, -0.1, np.nan]),
        }
        masks = win_analysis.filter_masks(signals, features)
        self.assertEqual(tuple(masks), win_analysis.FILTER_NAMES)
        self.assertEqual(masks['prior_close_not_limit'][0].tolist(), [True, False, False, True])
        self.assertEqual(masks['prior_close_not_limit'][1].tolist(), [True, True, False, True])
        self.assertEqual(masks['prior_rsi_60_to_80'][0].tolist(), [False, True, True, False])
        self.assertEqual(masks['prior_rsi_70_to_80'][0].tolist(), [False, False, True, False])
        self.assertEqual(masks['prior_atr_2_to_6_pct'][0].tolist(), [False, True, True, False])
        self.assertEqual(masks['prior_volume_gt_ma5'][1].tolist(), [True, True, False, True])
        self.assertEqual(masks['prior_k_ge_50'][0].tolist(), [False, True, True, False])
        signals['closed_at_limit'] = ~signals['closed_at_limit']
        signals['sale_ratio'] = signals['sale_ratio'][::-1]
        repeated = win_analysis.filter_masks(signals, features)
        for name in masks:
            np.testing.assert_array_equal(masks[name][0], repeated[name][0])
            np.testing.assert_array_equal(masks[name][1], repeated[name][1])

    def test_indicator_replay_is_unchanged_by_post_signal_prices(self):
        closing = np.linspace(10.0, 25.0, 90)
        adjusted = np.column_stack((closing, closing + 0.5, closing - 0.5, closing,
                                    np.linspace(1000.0, 2000.0, 90)))
        valid = np.ones(len(adjusted), dtype=bool)
        position = 65
        expected = analysis.features_before_signal(position, indicators.calculate(adjusted, valid),
                                                   volume.calculate(adjusted, valid))
        altered = adjusted.copy()
        altered[position:, :4] *= 4.0
        altered[position:, 4] *= 3.0
        actual = analysis.features_before_signal(position, indicators.calculate(altered, valid),
                                                 volume.calculate(altered, valid))
        for name in expected:
            self.assertEqual(expected[name], actual[name])

    def test_net_wins_exclude_losses_and_unresolved_trades(self):
        events = {
            'date': np.array(['2021-01-04'] * 3),
            'code': np.array(['sz000001', 'sz000002', 'sz000003']),
            'sale_ratio': np.array([[1.02] * 4, [1.005] * 4, [np.nan] * 4]),
            'delay': np.zeros((3, 4), dtype=np.int16),
            'closed_at_limit': np.array([True, True, False]),
        }
        mask = np.ones(3, dtype=bool)
        results = win_analysis.summarize_periods(events, mask, mask, analysis.period_masks(events['date']))
        stats = results['training_2021_2023']['20bp']
        self.assertEqual((stats['signals'], stats['closed'], stats['unresolved']), (3, 2, 1))
        self.assertEqual((stats['wins'], stats['not_wins'], stats['break_even']), (1, 1, 0))
        self.assertEqual(stats['win_rate'], 0.5)
        self.assertGreater(events['sale_ratio'][1, 0], 1)
        self.assertLess(study.net_returns(events['sale_ratio'][1, 0], 0.002), 0)
        self.assertFalse(study.eligible(stats, 2, 1, 1))

    def test_win_difference_is_paired_by_signal_day(self):
        dates = np.repeat(np.array([f'2021-01-{day:02}' for day in range(1, 26)]), 2)
        ratios = np.tile([1.02, 1.00], 25)
        events = {'date': dates, 'sale_ratio': np.repeat(ratios[:, None], 4, axis=1)}
        baseline = np.ones(len(dates), dtype=bool)
        selected = np.tile([True, False], 25)
        interval = win_analysis.win_interval(events, selected, baseline, samples=200)
        self.assertEqual(interval['win_rate_95'], [1.0, 1.0])
        self.assertEqual(interval['win_difference_vs_baseline_95'], [0.5, 0.5])
        with self.assertRaises(ValueError):
            win_analysis.win_interval(events, baseline, selected, samples=200)

    def test_small_samples_never_pass_profit_and_annual_gates(self):
        events = {
            'date': np.array(['2021-01-04', '2024-01-04']),
            'code': np.array(['sz300001', 'sz300002']),
            'prior_reference_valid': np.array([True, True]),
            'prior_limit': np.array([False, False]),
            'above_ma20': np.array([True, True]),
            'rsi6': np.array([60.0, 70.0]),
            'sale_ratio': np.array([[1.02] * 4, [0.90] * 4]),
            'delay': np.zeros((2, 4), dtype=np.int16),
            'closed_at_limit': np.array([True, False]),
        }
        features = {
            'volume_ratio': np.array([1.1, 1.1]), 'macd': np.array([1.0, 1.0]),
            'kdj_k': np.array([60.0, 70.0]), 'atr_ratio': np.array([0.03, 0.03]),
            'close_known': np.array([True, True]), 'close_up': np.array([True, True]),
            'dmi_difference': np.array([0.1, 0.1]),
        }
        results = win_analysis.evaluate(events, features, {'growth': np.ones(2, dtype=bool)})
        self.assertEqual(len(results), 15)
        self.assertTrue(all(not result['sample_ok'] for result in results))
        self.assertTrue(all(not result['strict_development_passed'] for result in results))

    def test_monthly_diagnostics_excludes_other_years_and_marks_other_months(self):
        events = {
            'date': np.array(['2023-09-01', '2024-09-02', '2024-10-08']),
            'code': np.array(['sz300001', 'sz300002', 'sz300003']),
            'prior_reference_valid': np.array([True, True, True]),
            'prior_limit': np.array([False, False, False]),
            'above_ma20': np.array([True, True, True]),
            'rsi6': np.array([60.0, 60.0, 60.0]),
            'sale_ratio': np.array([[1.02] * 4, [1.02] * 4, [0.90] * 4]),
            'delay': np.zeros((3, 4), dtype=np.int16),
            'closed_at_limit': np.array([True, True, False]),
        }
        features = {
            'volume_ratio': np.array([1.1, 1.1, 1.1]), 'macd': np.array([1.0, 1.0, 1.0]),
            'kdj_k': np.array([60.0, 60.0, 60.0]), 'atr_ratio': np.array([0.03, 0.03, 0.03]),
            'close_known': np.array([True, True, True]), 'close_up': np.array([True, True, True]),
            'dmi_difference': np.array([0.1, 0.1, 0.1]),
        }
        stats = win_analysis.monthly_diagnostics(events, features, {'growth': np.ones(3, dtype=bool)})
        september = stats['growth']['prior_k_ge_50']['2024-09']
        self.assertEqual(september['month_only']['wins'], 1)
        self.assertEqual(september['month_only']['closed'], 1)
        self.assertEqual(september['other_months']['wins'], 0)
        self.assertEqual(september['other_months']['closed'], 1)

    def test_board_limit_exit_proxy_can_be_worse_or_better_than_delayed_five_percent_exit(self):
        view = {'valid': np.array([True, True, True]), 'normal': np.array([True, True, True]),
                'raw_open': np.array([100, 94, 97]),
                'previous_close': np.array([100, 100, 94]),
                'down_five': np.array([95, 95, 89]),
                'adj_open': np.array([1.0, 0.94, 0.97])}
        ratio, delay = win_analysis.normal_board_open_ratio(view, 0, 1.0, 20)
        self.assertEqual((ratio, delay), (0.94, 0))
        self.assertEqual(exits.fixed_open_ratio(view, 0, 1.0, 1), 0.97)
        view['raw_open'][2] = 90
        view['adj_open'][2] = 0.90
        self.assertEqual(exits.fixed_open_ratio(view, 0, 1.0, 1), 0.90)
        self.assertGreater(ratio, exits.fixed_open_ratio(view, 0, 1.0, 1))
        with self.assertRaisesRegex(ValueError, 'invalid'):
            win_analysis.normal_board_open_ratio(view, 3, 1.0, 20)

    def test_ths_kdj_comparison_checks_both_partitions_and_local_values(self):
        def record(name, rows):
            answer = '| 股票代码 | kdj(k值)[20210104] |\n|---|---|\n'
            answer += ''.join(f'| {code} | {value} |\n' for code, value in rows)
            return {'date': '2021-01-05', 'previous_date': '2021-01-04', 'name': name,
                    'business_data': {'selectedSecuritiesCount': len(rows), 'answer': answer}}

        records = [record('base', [('300001.SZ', 51.0), ('300002.SZ', 49.0)]),
                   record('kdj_ge_50', [('300001.SZ', 51.0)]),
                   record('kdj_lt_50', [('300002.SZ', 49.0)])]
        events = {'date': np.array(['2021-01-05', '2021-01-05']),
                  'code': np.array(['sz300001', 'sz300002'])}
        features = {'kdj_k': np.array([50.9, 49.1])}
        candidates = {('sz300001', '2021-01-05'), ('sz300002', '2021-01-05')}
        calendar = np.array(['2021-01-04', '2021-01-05'])
        comparison = win_analysis.compare_ths_kdj({'records': records}, events, features, candidates, calendar)[0]
        self.assertTrue(comparison['partitions_disjoint'])
        self.assertTrue(comparison['partitions_cover_base'])
        self.assertEqual(comparison['local_common_count'], 2)
        self.assertEqual(comparison['classification_mismatches'], [])
        self.assertAlmostEqual(comparison['maximum_absolute_difference'], 0.1)
        features['kdj_k'][0] = 49.9
        mismatch = win_analysis.compare_ths_kdj({'records': records}, events, features, candidates, calendar)[0]
        self.assertEqual(mismatch['classification_mismatches'], ['sz300001'])
        records[1]['business_data']['answer'] = records[1]['business_data']['answer'].replace('20210104', '20261111')
        with self.assertRaisesRegex(ValueError, 'indicator date'):
            win_analysis.compare_ths_kdj({'records': records}, events, features, candidates, calendar)

    def test_ths_missing_partial_duplicate_or_holdout_evidence_is_rejected(self):
        partial = {'business_data': {'selectedSecuritiesCount': 1, 'answer': 'no results'}}
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            win_analysis.parse_ths_table(partial)
        duplicate = {'business_data': {'selectedSecuritiesCount': 2, 'answer':
                     '| 股票代码 |\n|---|\n| 300001.SZ |\n| 300001.SZ |'}}
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            win_analysis.parse_ths_table(duplicate)
        events = {'date': np.array([], dtype='<U10'), 'code': np.array([], dtype='<U8')}
        with self.assertRaisesRegex(ValueError, 'development dates'):
            win_analysis.compare_ths_kdj({'records': [{'date': '2025-01-02'}]}, events, {}, set(), np.array([]))

    def test_rejects_existing_result_before_reading_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = pathlib.Path(directory) / 'result.json'
            destination.write_text('original', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'overwrite'):
                win_analysis.run(SimpleNamespace(result=destination))
            self.assertEqual(destination.read_text(encoding='utf-8'), 'original')

    def test_rejects_mismatched_status_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            candidate = root / 'candidate.json'
            sample = root / 'sample.json'
            statuses = root / 'status.jsonl'
            reference = root / 'reference.json'
            for path in (candidate, sample, statuses):
                path.write_text('{}', encoding='utf-8')
            reference.write_text(json.dumps({
                'events_sha256': 'events', 'database_sha256': 'database',
                'candidate_sha256': 'incorrect', 'sample_sha256': study.digest_file(sample),
                'status_sha256': [study.digest_file(statuses)], 'complete_candidate_coverage': True,
                'holdout_signals_examined': False,
            }), encoding='utf-8')
            arguments = SimpleNamespace(candidates=candidate, sample=sample, statuses=[statuses], reference=reference)
            with self.assertRaisesRegex(ValueError, 'provenance mismatch'):
                win_analysis.load_evidence(arguments, {'events_sha256': 'events', 'database_sha256': 'database'})


if __name__ == '__main__':
    unittest.main()
