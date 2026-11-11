import argparse
import json
import pathlib
import tempfile
import unittest

import numpy as np

import limit_up_momentum_boll as research
import limit_up_study as study


def features():
    return {
        'previous_known': np.array([True, True, True, True, True, False]),
        'previous_sealed': np.array([False, False, True, True, False, False]),
        'earlier_known': np.array([True, True, True, True, True, False]),
        'earlier_sealed': np.array([False, False, False, True, False, False]),
        'macd': np.array([0.2, -1, 0.2, 0.2, 0.2, 0.2]),
        'earlier_macd': np.array([0.1, -2, 0.1, 0.1, 0.1, 0.1]),
        'rsi': np.array([50., 30., 55., 84.99, 75., 55.]),
        'earlier_rsi': np.array([49., 29., 54., 84., 74., 54.]),
        'close': np.array([10., 9., 10., 10., 10., 10.]),
        'middle': np.full(6, 10.),
        'upper': np.full(6, 11.),
        'lower': np.full(6, 9.),
        'dmi': np.array([1., -1., 0., np.nan, 1., 1.]),
        'atr': np.array([0.02, 0.03, 0.06, np.nan, 0.059, 0.01]),
    }


class MomentumBollTests(unittest.TestCase):
    def test_four_branches_use_known_prior_states_and_strict_boundaries(self):
        masks = research.branch_masks(features())
        np.testing.assert_array_equal(masks['first_trend'], [True, False, False, False, False, False])
        np.testing.assert_array_equal(masks['first_rebound'], [False, True, False, False, False, False])
        np.testing.assert_array_equal(masks['one_to_two'], [False, False, True, False, False, False])
        np.testing.assert_array_equal(masks['two_or_more'], [False, False, False, True, False, False])

    def test_alternate_rsi_ranges_are_separate(self):
        masks = research.branch_masks(features(), alternate=True)
        self.assertFalse(masks['first_trend'][0])
        self.assertTrue(masks['first_trend'][4])
        self.assertFalse(masks['first_rebound'][1])
        self.assertFalse(masks['one_to_two'][2])

    def test_unknown_prior_state_cannot_be_classified_as_a_board(self):
        data = features()
        data['earlier_known'][2] = False
        self.assertFalse(research.branch_masks(data)['one_to_two'][2])
        data['earlier_sealed'][2] = True
        with self.assertRaises(ValueError):
            research.branch_masks(data)

    def test_dmi_and_atr_filter_boundary_and_missing_data(self):
        masks = research.enhancement_masks(features())
        np.testing.assert_array_equal(masks['dmi'], [True, False, False, False, True, True])
        np.testing.assert_array_equal(masks['atr'], [True, True, False, False, True, False])
        np.testing.assert_array_equal(masks['dmi_atr'], [True, False, False, False, True, False])

    def test_bollinger_uses_full_window_and_population_deviation(self):
        values = np.arange(1., 22.)
        middle, upper, lower = research.bollinger(values, np.ones(len(values), dtype=bool))
        self.assertTrue(np.isnan(middle[18]))
        self.assertAlmostEqual(middle[19], 10.5)
        self.assertAlmostEqual(upper[19], 10.5 + 2 * np.std(values[:20], ddof=0))
        self.assertAlmostEqual(lower[19], 10.5 - 2 * np.std(values[:20], ddof=0))
        valid = np.ones(len(values), dtype=bool)
        valid[5] = False
        self.assertTrue(np.isnan(research.bollinger(values, valid)[0][-1]))

    def test_bollinger_before_signal_ignores_future_prices(self):
        values = np.linspace(10., 30., 100)
        valid = np.ones(len(values), dtype=bool)
        before = research.bollinger(values, valid)
        values[80:] *= 8
        after = research.bollinger(values, valid)
        for left, right in zip(before, after):
            np.testing.assert_equal(left[:80], right[:80])

    def test_bollinger_sample_deviation_is_explicit_and_validated(self):
        values = np.arange(1., 21.)
        valid = np.ones(len(values), dtype=bool)
        middle, upper, lower = research.bollinger(values, valid, ddof=1)
        self.assertAlmostEqual(middle[-1], 10.5)
        self.assertAlmostEqual(upper[-1], 10.5 + 2 * np.std(values, ddof=1))
        self.assertAlmostEqual(lower[-1], 10.5 - 2 * np.std(values, ddof=1))
        for ddof in (-1, 20, 1.5):
            with self.assertRaises(ValueError):
                research.bollinger(values, valid, ddof=ddof)

    def test_stop_rule_is_inclusive_and_unknown_exit_stays_unknown(self):
        events = {'date': np.array(['2021-01-04'] * 4),
                  'sale_ratio': np.array([[1, 2, 3, 4]] * 4, dtype=float),
                  'delay': np.array([[0, 1, 2, 3]] * 4, dtype=np.int16)}
        ratios, delays = research.weak_exit(events, np.array([0.96, 0.961, np.nan, 0.0]))
        np.testing.assert_equal(ratios, [2, 3, np.nan, np.nan])
        np.testing.assert_equal(delays, [1, 2, -1, -1])

    def test_unknown_st_is_not_treated_as_non_st(self):
        events = {'code': np.array(['sz000001', 'sz000002', 'sz000003']),
                  'date': np.array(['2021-01-04'] * 3)}
        non_st, st, unknown = research.status_masks(
            events, {('sz000001', '2021-01-04'): False, ('sz000002', '2021-01-04'): True})
        np.testing.assert_equal(non_st, [True, False, False])
        np.testing.assert_equal(st, [False, True, False])
        np.testing.assert_equal(unknown, [False, False, True])
        with self.assertRaises(ValueError):
            research.status_masks(events, {('sz000001', '2021-01-04'): 0})

    def test_market_results_separate_main_growth_and_technology(self):
        base = features()
        data = {key: np.repeat(value[:1], 3) for key, value in base.items()}
        data.update({'next_close_ratio': np.ones(3), 'hold3_ratio': np.ones(3),
                     'hold5_ratio': np.ones(3), 'hold3_delay': np.zeros(3, dtype=np.int16),
                     'hold5_delay': np.zeros(3, dtype=np.int16)})
        codes = np.array(['sh600001', 'sz300001', 'sh688001'])
        dates = np.array(['2021-01-04'] * 3)
        events = {'code': codes, 'date': dates, 'board': np.array([10, 20, 20]),
                  'sale_ratio': np.ones((3, 4)), 'delay': np.zeros((3, 4), dtype=np.int16),
                  'closed_at_limit': np.ones(3, dtype=bool)}
        statuses = {(str(code), str(date)): False for code, date in zip(codes, dates)}
        result = research.evaluate(events, data, statuses)[0]['groups']
        self.assertEqual(result['training_2021_2023/all']['status_known_non_st'], 3)
        for market in ('main', 'growth', 'technology'):
            self.assertEqual(result['training_2021_2023/' + market]['status_known_non_st'], 1)

    def test_frozen_hash_mismatch_is_rejected_before_replay(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            files = {}
            for name in ('database', 'calendar', 'manifest', 'events.npz'):
                files[name] = root / name
                files[name].write_bytes(name.encode())
            metadata = {'event_schema_version': study.EVENT_SCHEMA_VERSION, 'limited': False,
                        'database_sha256': 'incorrect',
                        'calendar_sha256': study.digest_file(files['calendar']),
                        'manifest_sha256': study.digest_file(files['manifest']),
                        'events_sha256': study.digest_file(files['events.npz'])}
            (root / 'extract.json').write_text(json.dumps(metadata), encoding='utf-8')
            args = argparse.Namespace(input=root, database=files['database'],
                                      calendar=files['calendar'], manifest=files['manifest'])
            with self.assertRaisesRegex(ValueError, 'hash mismatch'):
                research.check_inputs(args)

    def test_existing_result_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / 'result.json'
            output.write_text('unchanged', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'overwrite'):
                research.run(argparse.Namespace(result=output))
            self.assertEqual(output.read_text(encoding='utf-8'), 'unchanged')


if __name__ == '__main__':
    unittest.main()
