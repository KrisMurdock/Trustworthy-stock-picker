import datetime
import itertools
import json
import pathlib
import sqlite3
import tempfile
import unittest

import numpy as np

import limit_up_minutes as minute_sample
import limit_up_path_analysis as path_analysis


def fixture():
    date = '2023-04-28'
    bars = []
    for end in path_analysis.session_ends(date):
        bars.append({'end': end.isoformat() + '+08:00', 'open': 10.0, 'high': 10.3,
                     'low': 9.8, 'close': 10.0, 'volume_raw': 100, 'final': True})
    bars[3]['high'] = 12.0
    bars[3]['close'] = 12.0
    bars[-1].update({'open': 11.6, 'high': 11.8, 'low': 11.5, 'close': 11.7})
    record = {'code': 'sz300031', 'date': date, 'daily_cents': [1000, 1200, 980, 1170]}
    archive = {'code': record['code'], 'source': 'baostock', 'period_minutes': 5,
               'base_period_minutes': 5, 'adjust': 'none', 'from': date, 'through': date, 'bars': bars}
    return record, {'status': 'ok', 'archive': archive}


class MinutePathTests(unittest.TestCase):
    def test_candidate_export_is_versioned_and_does_not_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'candidates.json'
            records = [{'code': 'sz300001', 'date': '2021-01-04', 'daily_cents': [10, 12, 9, 11]}]
            minute_sample.export_candidates(path, records, 'events-hash')
            self.assertEqual(json.loads(path.read_text(encoding='utf-8')),
                             {'schema_version': 1, 'events_sha256': 'events-hash', 'records': records})
            with self.assertRaises(FileExistsError):
                minute_sample.export_candidates(path, records, 'events-hash')

    def test_fixed_sample_is_deterministic_and_ignores_outcome_fields(self):
        records = [{'code': f'sz30000{index}', 'date': f'{year}-01-0{index}', 'outcome': index}
                   for year in minute_sample.YEARS for index in range(1, 4)]
        selected, available = minute_sample.sample_records(records, per_year=1)
        for record in records:
            record['outcome'] *= -100
        repeated, repeated_available = minute_sample.sample_records(list(reversed(records)), per_year=1)
        self.assertEqual([(item['code'], item['date']) for item in selected],
                         [(item['code'], item['date']) for item in repeated])
        self.assertEqual(available, repeated_available)
        self.assertEqual(len(selected), 4)

    def test_sample_rejects_reserved_years_and_duplicates(self):
        records = [{'code': 'sz300001', 'date': f'{year}-01-01'} for year in minute_sample.YEARS]
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            minute_sample.sample_records(records + [records[0]], per_year=1)
        with self.assertRaisesRegex(ValueError, 'development'):
            minute_sample.sample_records(records + [{'code': 'sz300001', 'date': '2025-01-01'}], per_year=1)

    def test_matched_path_uses_first_completed_bar_not_future_close(self):
        record, response = fixture()
        result = path_analysis.classify_path(record, response)
        self.assertEqual(result['quality'], 'matched')
        self.assertEqual(result['first_touch_bar_end'], '09:50')
        self.assertTrue(result['first_touch_close_at_limit'])
        self.assertFalse(result['closed_at_limit'])
        self.assertTrue(path_analysis.first_close_confirmation(result))
        response['archive']['bars'][3]['close'] = 11.9
        result = path_analysis.classify_path(record, response)
        self.assertFalse(result['first_touch_close_at_limit'])
        self.assertFalse(path_analysis.first_close_confirmation(result))

    def test_last_bar_is_not_actionable_after_its_close(self):
        record, response = fixture()
        response['archive']['bars'][3].update({'high': 10.3, 'close': 10.0})
        response['archive']['bars'][-1].update({'high': 12.0, 'close': 12.0})
        record['daily_cents'][-1] = 1200
        result = path_analysis.classify_path(record, response)
        self.assertEqual(result['first_touch_bar_end'], '15:00')
        self.assertTrue(result['first_touch_last_bar'])
        self.assertFalse(path_analysis.first_close_confirmation(result))

    def test_missing_or_incomplete_minutes_are_not_matches(self):
        record, response = fixture()
        self.assertEqual(path_analysis.classify_path(record, None)['quality'], 'missing_result')
        response['archive']['bars'].pop()
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'incomplete_session')
        record, response = fixture()
        response['archive']['bars'][2]['end'] = response['archive']['bars'][1]['end']
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'incomplete_session')

    def test_daily_price_disagreement_and_wrong_timezone_are_rejected(self):
        record, response = fixture()
        record['daily_cents'][0] += 1
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'daily_ohlc_mismatch')
        record, response = fixture()
        response['archive']['bars'][0]['end'] = datetime.datetime.fromisoformat(
            response['archive']['bars'][0]['end']).replace(tzinfo=datetime.timezone.utc).isoformat()
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'incomplete_session')

    def test_mismatch_components_are_diagnostic_and_keep_quality_failed(self):
        record, response = fixture()
        record['daily_cents'][2] -= 3
        record['daily_cents'][3] += 1
        path = dict(record, **path_analysis.classify_path(record, response))
        result = path_analysis.mismatch_component_diagnostics(
            [path, {'quality': 'source_unavailable'}], {(record['code'], record['date']): response})
        self.assertEqual(path['quality'], 'daily_ohlc_mismatch')
        self.assertEqual(result['mismatched_sessions'], 1)
        self.assertEqual(result['field_mismatch_counts'], {'open': 0, 'high': 0, 'low': 1, 'close': 1})
        self.assertEqual(result['details'][0]['delta_cents'], [0, 0, 3, -1])

    def test_unavailable_and_wrong_source_are_explicit(self):
        record, response = fixture()
        response['status'] = 'fetch_failed'
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'source_unavailable')
        record, response = fixture()
        response['archive']['source'] = 'other'
        self.assertEqual(path_analysis.classify_path(record, response)['quality'], 'archive_metadata_mismatch')

    def test_retry_may_replace_failure_but_never_a_success(self):
        key = ('sz300031', '2023-04-28')
        with tempfile.TemporaryDirectory() as directory:
            first = pathlib.Path(directory) / 'first.jsonl'
            second = pathlib.Path(directory) / 'second.jsonl'
            first.write_text(json.dumps({'code': key[0], 'date': key[1], 'status': 'fetch_failed'}) + '\n')
            second.write_text(json.dumps({'code': key[0], 'date': key[1], 'status': 'ok'}) + '\n')
            responses = path_analysis.read_responses([first, second], {key})
            self.assertEqual(responses[key]['status'], 'ok')
            with self.assertRaisesRegex(ValueError, 'duplicate'):
                path_analysis.read_responses([second, first], {key})
            with self.assertRaisesRegex(ValueError, 'unexpected'):
                path_analysis.read_responses([second], set())

    def test_daily_ohlc_is_verified_against_frozen_database(self):
        record, _ = fixture()
        connection = sqlite3.connect(':memory:')
        self.addCleanup(connection.close)
        connection.execute('CREATE TABLE daily_bar (code TEXT,trade_date TEXT,adjust TEXT,data_field TEXT,'
                           'open_micros INTEGER,high_micros INTEGER,low_micros INTEGER,close_micros INTEGER)')
        connection.execute('INSERT INTO daily_bar VALUES (?,?,?,?,?,?,?,?)',
                           (record['code'], record['date'], 'none', 'day', 10000000, 12000000, 9800000, 11700000))
        path_analysis.verify_daily_prices(connection, [record])
        record['daily_cents'][1] += 1
        with self.assertRaisesRegex(ValueError, 'frozen daily database'):
            path_analysis.verify_daily_prices(connection, [record])

    def test_unknown_confirmation_pf_bounds_cover_extreme_assignments(self):
        result = path_analysis.confirmation_pf_bounds([0.1, -0.1], [0.2, -0.05, 0])
        self.assertTrue(result['available'])
        self.assertEqual((result['confirmed'], result['unknown']), (2, 3))
        self.assertEqual((result['unknown_wins'], result['unknown_losses'], result['unknown_flat']), (1, 1, 1))
        self.assertAlmostEqual(result['lower_pf'], 2 / 3)
        self.assertAlmostEqual(result['upper_pf'], 3)

    def test_unknown_confirmation_pf_bounds_match_all_possible_assignments(self):
        confirmed = np.array([0.07, -0.06])
        unknown = np.array([0.2, -0.05, 0.01, -0.03])
        result = path_analysis.confirmation_pf_bounds(confirmed, unknown)
        factors = []
        for included in itertools.product((False, True), repeat=len(unknown)):
            returns = np.concatenate((confirmed, unknown[np.asarray(included)]))
            factors.append(np.maximum(returns, 0).sum() / np.maximum(-returns, 0).sum())
        self.assertAlmostEqual(result['lower_pf'], min(factors))
        self.assertAlmostEqual(result['upper_pf'], max(factors))

    def test_unknown_confirmation_pf_bounds_refuse_unresolved_or_one_sided_data(self):
        result = path_analysis.confirmation_pf_bounds([0.1, -0.1], [float('nan')])
        self.assertEqual(result, {'available': False, 'reason': 'unresolved_returns'})
        result = path_analysis.confirmation_pf_bounds([0.1], [-0.1])
        self.assertEqual(result, {'available': False, 'reason': 'confirmed_gains_and_losses_required'})
        with self.assertRaisesRegex(ValueError, 'one-dimensional'):
            path_analysis.confirmation_pf_bounds([[0.1, -0.1]], [])

    def test_coverage_diagnostics_include_all_failed_quality_records(self):
        dates = np.array(['2024-01-01', '2024-01-02', '2024-01-03', '2024-01-04'])
        codes = np.array(['sz300001', 'sz300002', 'sz300003', 'sz300004'])
        ratios = np.ones((4, len(path_analysis.study.HOLDS)))
        ratios[:, path_analysis.study.HOLDS.index(3)] = [1.2, 0.8, 1.3, 0.8]
        events = {'date': dates, 'code': codes, 'sale_ratio': ratios,
                  'delay': np.zeros_like(ratios), 'closed_at_limit': np.zeros(4, dtype=bool)}
        lookup = {(code, date): index for index, (code, date) in enumerate(zip(codes, dates))}
        qualities = ['matched', 'daily_ohlc_mismatch', 'source_unavailable', 'matched']
        paths = [{'code': code, 'date': date, 'quality': quality}
                 for code, date, quality in zip(codes, dates, qualities)]
        result = path_analysis.coverage_outcome_diagnostics(events, lookup, paths)
        self.assertEqual(result['all_sampled']['signals'], 4)
        self.assertEqual(result['unknown_quality']['signals'], 2)
        self.assertEqual(result['unknown_quality']['wins'], 1)
        self.assertEqual(result['unknown_quality']['losses'], 1)


if __name__ == '__main__':
    unittest.main()
