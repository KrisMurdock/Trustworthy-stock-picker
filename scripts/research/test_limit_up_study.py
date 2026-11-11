import datetime
import contextlib
import gzip
import hashlib
import io
import json
import pathlib
import tempfile
import unittest

import numpy as np

import limit_up_study as study


def price_fixture():
    dates = np.asarray([(datetime.date(2024, 1, 1) + datetime.timedelta(days=index)).isoformat()
                        for index in range(100)])
    raw = np.full((len(dates), 5), 100_000_000, dtype=np.int64)
    raw[:, :4] = 10_000_000
    adjusted = np.full((len(dates), 5), 10.0)
    adjusted[:, 4] = 100_000_000
    return dates, raw, adjusted


def set_bar(raw, adjusted, index, opening, high, low, close):
    prices = np.asarray([opening, high, low, close])
    raw[index, :4] = np.rint(prices * 1_000_000).astype(np.int64)
    adjusted[index, :4] = prices


def fake_events():
    return {
        'code': np.asarray(['sh600000', 'sh600001', 'sh600000', 'sh600001', 'sh600002']),
        'date': np.asarray(['2021-01-04', '2021-01-05', '2024-01-04', '2024-01-05', '2025-01-04']),
        'board': np.asarray([10, 10, 10, 10, 10]),
        'prior_reference_valid': np.asarray([True] * 5),
        'prior_limit': np.asarray([False] * 5),
        'above_ma20': np.asarray([True] * 5),
        'ma20_above_ma60': np.asarray([True] * 5),
        'rsi6': np.asarray([60.0] * 5),
        'gap': np.asarray([0.01] * 5),
        'closed_at_limit': np.asarray([True, False, True, False, True]),
        'sale_ratio': np.asarray([[1.1] * 4, [0.95] * 4, [1.1] * 4, [0.95] * 4, [1.01] * 4]),
        'delay': np.zeros((5, 4), dtype=np.int16),
    }


class LimitUpStudyTests(unittest.TestCase):
    def test_half_up_price_rounding_and_board_classification(self):
        np.testing.assert_array_equal(study.limit_cents([1015, 1000], 10), [1117, 1100])
        np.testing.assert_array_equal(study.limit_cents([1050, 1000], -5), [998, 950])
        self.assertEqual(study.limit_board('sh603000'), 10)
        self.assertEqual(study.limit_board('sz300001'), 20)
        self.assertEqual(study.limit_board('sh688001'), 20)
        self.assertIsNone(study.limit_board('bj430001'))

    def test_rsi_uses_past_and_restarts_after_missing_data(self):
        values = study.rsi6(np.asarray([1., 2., 1., 99., 3., 2.]),
                            np.asarray([True, True, True, False, True, True]))
        self.assertTrue(np.isnan(values[0]))
        self.assertAlmostEqual(values[1], 100)
        self.assertAlmostEqual(values[2], 100 * 5 / 6)
        self.assertTrue(np.isnan(values[4]))
        self.assertAlmostEqual(values[5], 0)

    def test_opened_below_limit_and_touched_enters_at_board(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 10.50)
        set_bar(raw, adjusted, 63, 10.80, 10.90, 10.70, 10.80)
        events, quality = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(quality['touches'], 1)
        self.assertEqual(events['date'][0], dates[62])
        self.assertFalse(events['closed_at_limit'][0])
        self.assertAlmostEqual(events['sale_ratio'][0, 0], 10.8 / 11)
        self.assertEqual(events['delay'][0, 0], 0)

    def test_open_at_limit_is_not_assumed_filled(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 11.00, 11.00, 10.90, 11.00)
        events, quality = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(len(events['date']), 0)
        self.assertEqual(quality['open_at_limit_or_higher'], 1)

    def test_unknown_previous_limit_status_is_not_a_first_board(self):
        dates, raw, adjusted = price_fixture()
        adjusted[61, :4] *= 0.9
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 10.50)
        adjusted[62, :4] *= 0.9
        events, quality = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(quality['touches'], 1)
        self.assertFalse(events['prior_reference_valid'][0])
        config = {'board': 10, 'prior': 'first', 'trend': 'any',
                  'rsi': 'any', 'gap': 'any', 'hold': 1}
        self.assertFalse(study.select_mask(events, config)[0])

    def test_legacy_events_without_prior_status_fail_closed(self):
        events = fake_events()
        del events['prior_reference_valid']
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'events.npz'
            np.savez(path, **events)
            with self.assertRaisesRegex(ValueError, 'prior_reference_valid'):
                study.load_events(path)

    def test_holdout_is_not_opened_for_failed_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            (output / 'selection.json').write_text(json.dumps({
                'train_selection_target_passes': 0,
                'candidate': {'score': 0.5},
            }), encoding='utf-8')
            arguments = type('Arguments', (), {'output': output})()
            with self.assertRaisesRegex(ValueError, 'holdout'):
                study.finalize(arguments)

    def test_development_exit_embargo_covers_all_holds_and_ignores_holdout_returns(self):
        dates = np.asarray(['2024-12-27', '2024-12-30', '2024-12-31', '2025-01-02',
                            '2025-01-03', '2025-01-06', '2025-01-07', '2025-01-08', '2025-01-09'])
        events = {'date': np.asarray(['2024-12-30', '2025-01-02']),
                  'delay': np.asarray([[0, 0, 1, 0], [0, 0, 0, 0]]),
                  'sale_ratio': np.ones((2, 4))}
        cutoff = study.development_exit_cutoff(events, dates)
        self.assertEqual(cutoff, '2025-01-07')
        events['sale_ratio'][1] = np.nan
        events['delay'][1] = -1
        self.assertEqual(study.development_exit_cutoff(events, dates), cutoff)

    def test_embargo_rejects_unresolved_and_missing_calendar(self):
        dates = np.asarray(['2024-12-30', '2024-12-31', '2025-01-02', '2025-01-03',
                            '2025-01-06', '2025-01-07'])
        events = {'date': np.asarray(['2024-12-30']), 'delay': np.zeros((1, 4), dtype=int),
                  'sale_ratio': np.ones((1, 4))}
        events['delay'][0, 0] = -1
        with self.assertRaisesRegex(ValueError, 'resolved'):
            study.development_exit_cutoff(events, dates)
        events['delay'][0, 0] = 0
        with self.assertRaisesRegex(ValueError, 'missing'):
            study.development_exit_cutoff(events, dates[1:])
        with self.assertRaisesRegex(ValueError, 'cover'):
            study.development_exit_cutoff(events, dates[:-1])

    def test_finalize_excludes_signals_until_all_development_exits(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            calendar = output / 'calendar.csv'
            calendar.write_text('\n'.join(['2024-12-30', '2024-12-31', '2025-01-02', '2025-01-03',
                                           '2025-01-06', '2025-01-07', '2025-01-08', '2025-01-09']) + '\n')
            count = 3
            events = {'code': np.asarray(['sh600000', 'sh600001', 'sh600002']),
                      'date': np.asarray(['2024-12-30', '2025-01-02', '2025-01-08']),
                      'board': np.asarray([10] * count), 'prior_reference_valid': np.ones(count, dtype=bool),
                      'prior_limit': np.zeros(count, dtype=bool), 'above_ma20': np.ones(count, dtype=bool),
                      'ma20_above_ma60': np.ones(count, dtype=bool), 'rsi6': np.full(count, 60.0),
                      'gap': np.full(count, 0.01), 'closed_at_limit': np.ones(count, dtype=bool),
                      'sale_ratio': np.asarray([[1.1] * 4, [0.8] * 4, [1.1] * 4]),
                      'delay': np.zeros((count, 4), dtype=int)}
            np.savez(output / 'events.npz', **events)
            event_hash = study.digest_file(output / 'events.npz')
            config = {'board': 10, 'prior': 'any', 'trend': 'any', 'rsi': 'any', 'gap': 'any', 'hold': 1}
            (output / 'selection.json').write_text(json.dumps({'events_sha256': event_hash,
                'train_selection_target_passes': 1, 'candidate': {'score': 1.1, 'config': config}}))
            (output / 'extract.json').write_text(json.dumps({'events_sha256': event_hash,
                                                            'calendar_sha256': study.digest_file(calendar)}))
            arguments = type('Arguments', (), {'output': output, 'calendar': calendar})()
            with contextlib.redirect_stdout(io.StringIO()):
                study.finalize(arguments)
            result = json.loads((output / 'holdout.json').read_text())
            self.assertEqual(result['embargo_through'], '2025-01-07')
            self.assertEqual(result['periods']['holdout_2025_2026']['20bp']['signals'], 1)
            self.assertEqual(result['periods']['year_2025']['20bp']['signals'], 1)

    def test_clustered_uncertainty_is_seeded_and_excludes_other_periods(self):
        dates = np.asarray([f'2024-01-{day:02d}' for day in range(1, 26)])
        ratios = np.tile(np.asarray([1.1, 0.95] * 13)[:25, None], (1, 4))
        events = {'date': dates, 'sale_ratio': ratios}
        chosen = np.arange(25) < 20
        first = study.bootstrap_pf(events, chosen, 1, samples=100)
        events['sale_ratio'][20:] = 100
        second = study.bootstrap_pf(events, chosen, 1, samples=100)
        self.assertEqual(first, second)
        self.assertEqual(first['method'], 'signal-day clustered bootstrap')

    def test_same_day_close_does_not_select_the_event(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 10.50)
        set_bar(raw, adjusted, 63, 10.80, 10.90, 10.70, 10.80)
        first, _ = study.extract_stock('sh600000', dates, raw, adjusted)
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 11.00)
        second, _ = study.extract_stock('sh600000', dates, raw, adjusted)
        for name in ('date', 'board', 'prior_reference_valid', 'prior_limit', 'above_ma20', 'ma20_above_ma60', 'rsi6', 'gap'):
            np.testing.assert_equal(first[name], second[name])
        self.assertNotEqual(first['closed_at_limit'][0], second['closed_at_limit'][0])

    def test_unavailable_or_lower_limit_open_delays_exit(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 10.50)
        set_bar(raw, adjusted, 63, 9.98, 10.10, 9.98, 10.00)
        set_bar(raw, adjusted, 64, 10.40, 10.50, 10.30, 10.40)
        events, _ = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(events['delay'][0, 0], 1)
        self.assertAlmostEqual(events['sale_ratio'][0, 0], 10.4 / 11)
        raw[63] = 0
        adjusted[63] = np.nan
        events, _ = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(events['delay'][0, 0], 1)
        self.assertAlmostEqual(events['sale_ratio'][0, 0], 10.4 / 11)

    def test_suspected_ex_rights_and_missing_history_are_rejected(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.05, 11.00, 10.05, 10.50)
        adjusted[62, :4] *= 0.5
        events, quality = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(len(events['date']), 0)
        self.assertGreater(quality['suspected_action_rows'], 0)
        adjusted[62, :4] *= 2
        raw[55] = 0
        adjusted[55] = np.nan
        events, _ = study.extract_stock('sh600000', dates, raw, adjusted)
        self.assertEqual(len(events['date']), 0)

    def test_metrics_report_unresolved_and_costed_profit_factor(self):
        events = fake_events()
        events['sale_ratio'][4, 0] = np.nan
        mask = np.asarray([True, True, False, False, True])
        stats = study.summarize(events, mask, 1, 0.002)
        expected = study.net_returns(np.asarray([1.1, 0.95]), 0.002)
        self.assertEqual((stats['signals'], stats['closed'], stats['unresolved']), (3, 2, 1))
        self.assertEqual(stats['win_rate'], 0.5)
        self.assertAlmostEqual(stats['pf'], expected[0] / -expected[1])
        self.assertAlmostEqual(stats['payoff'], expected[0] / -expected[1])

    def test_candidate_selection_never_reads_holdout_returns(self):
        events = fake_events()
        minimums = (1, 1, 1, 1, 1, 1)
        tested, first = study.find_candidates(events, minimums)
        events['sale_ratio'][4] = [20., 0.01, 20., 0.01]
        _, second = study.find_candidates(events, minimums)
        self.assertEqual(tested, 648)
        self.assertTrue(first)
        self.assertEqual(first[0], second[0])

    def test_adjusted_data_hash_order_and_asof_are_checked(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'prices.json.gz'
            payload = {'schema_version': 1, 'code': 'sh600000', 'source': 'sina', 'adjust': 'qfq',
                       'data_field': 'qfqday', 'as_of': '2026-10-30', 'bars': [
                           {'date': '2024-01-01', 'open': 10, 'high': 10, 'low': 10,
                            'close': 10, 'volume_raw': 100}]}
            contents = gzip.compress(json.dumps(payload).encode())
            path.write_bytes(contents)
            digest = hashlib.sha256(contents).hexdigest()
            values = study.read_adjusted(path, 'sh600000', {'2024-01-01': 0}, digest, '2026-10-30')
            self.assertEqual(values[0, 3], 10)
            with self.assertRaisesRegex(ValueError, 'hash mismatch'):
                study.read_adjusted(path, 'sh600000', {'2024-01-01': 0}, 'wrong', '2026-10-30')
            with self.assertRaisesRegex(ValueError, 'metadata'):
                study.read_adjusted(path, 'sh600000', {'2024-01-01': 0}, digest, '2026-11-01')


if __name__ == '__main__':
    unittest.main()
