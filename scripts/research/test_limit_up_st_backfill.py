import contextlib
import io
import json
import pathlib
import socket
import tempfile
import types
import unittest
from unittest.mock import patch

import limit_up_minutes as minute_sample
import limit_up_st_analysis as analysis
import limit_up_st_backfill as backfill
import limit_up_study as study


class Response:
    def __init__(self, rows, error_code='0'):
        self.error_code = error_code
        self.fields = ['date', 'code', 'isST', 'tradestatus']
        self.rows = rows
        self.index = -1

    def next(self):
        self.index += 1
        return self.index < len(self.rows)

    def get_row_data(self):
        return self.rows[self.index]


class Client:
    def __init__(self, rows=None, login_error='0', query_error='0'):
        self.rows = rows or []
        self.login_error = login_error
        self.query_error = query_error
        self.logouts = 0
        self.queries = []

    def login(self):
        return types.SimpleNamespace(error_code=self.login_error)

    def logout(self):
        self.logouts += 1

    def query_history_k_data_plus(self, code, fields, **kwargs):
        self.queries.append((code, fields, kwargs))
        rows = [row for row in self.rows if row[1] == code
                and kwargs['start_date'] <= row[0] <= kwargs['end_date']]
        return Response(rows, self.query_error)


class HistoricalSTBackfillTests(unittest.TestCase):
    def candidate_fixture(self, root):
        records = []
        for year in range(2021, 2025):
            for day in range(1, 26):
                code = 'sz300001' if day % 2 else 'sh688002'
                records.append({'code': code, 'date': f'{year}-01-{day:02}',
                                'daily_cents': [10, 12, 9, 11]})
        sampled, available = minute_sample.sample_records(records)
        candidate, sample = root / 'candidates.json', root / 'sample.json'
        candidate.write_text(json.dumps({'schema_version': 1, 'events_sha256': 'events', 'records': records}))
        sample.write_text(json.dumps({'schema_version': 1, 'events_sha256': 'events',
                                      'records': sampled, 'available_by_year': available}))
        return records, sampled, candidate, sample

    def provider_rows(self, records):
        return [[record['date'], record['code'][:2] + '.' + record['code'][2:],
                 '1' if record['code'].startswith('sh') else '0', '1'] for record in records]

    def test_group_order_is_deterministic_and_skips_successful_events(self):
        keys = {('sz300001', '2021-01-04'), ('sz300001', '2021-02-01'),
                ('sz300001', '2022-01-04'), ('sh688002', '2021-01-04')}
        excluded = {('sz300001', '2021-02-01')}
        groups = backfill.ordered_groups(keys, excluded)
        self.assertEqual(groups, backfill.ordered_groups(set(reversed(sorted(keys))), excluded))
        self.assertEqual({(code, date) for code, dates in groups for date in dates}, keys - excluded)
        self.assertTrue(all(len({date[:4] for date in dates}) == 1 for _, dates in groups))

    def test_provider_response_requires_unique_valid_trading_status(self):
        dates = ['2021-01-04', '2021-01-05', '2021-01-06', '2021-01-07', '2021-01-08']
        code = 'sz.300001'
        rows = [[dates[0], code, '0', '1'], [dates[1], code, '1', '1'],
                [dates[2], code, '0', '0'], [dates[3], code, '0', '1'],
                [dates[3], code, '0', '1'], [dates[4], code, '', '1']]
        client = Client(rows)
        result = backfill.fetch_group(client, 'sz300001', dates)
        self.assertEqual(result[dates[0]], {'status': 'ok', 'is_st': False})
        self.assertEqual(result[dates[1]], {'status': 'ok', 'is_st': True})
        self.assertTrue(all(result[date]['status'] == 'unknown' for date in dates[2:]))
        self.assertEqual(client.logouts, 1)
        self.assertEqual(client.queries[0][2]['adjustflag'], '3')
        with self.assertRaisesRegex(ValueError, 'stock-year'):
            backfill.fetch_group(client, 'sz300001', ['2021-01-01', '2022-01-01'])

    def test_login_query_and_oversize_failures_remain_unknown(self):
        dates = ['2021-01-04']
        login_failure = Client(login_error='1')
        self.assertEqual(backfill.fetch_group(login_failure, 'sz300001', dates)[dates[0]]['status'], 'unknown')
        self.assertEqual(login_failure.logouts, 0)
        query_failure = Client(query_error='1')
        self.assertEqual(backfill.fetch_group(query_failure, 'sz300001', dates)[dates[0]]['status'], 'unknown')
        self.assertEqual(query_failure.logouts, 1)
        oversized = Client([[dates[0], 'sz.300001', '0', '1']] * 253)
        self.assertEqual(backfill.fetch_group(oversized, 'sz300001', dates)[dates[0]]['status'], 'unknown')
        self.assertEqual(oversized.logouts, 1)

    def test_partial_batch_exits_nonzero_and_resume_is_complete(self):
        original_timeout = socket.getdefaulttimeout()
        self.addCleanup(socket.setdefaulttimeout, original_timeout)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            records, sampled, candidate, sample = self.candidate_fixture(root)
            first, second = root / 'first.jsonl', root / 'second.jsonl'
            client = Client(self.provider_rows(records))
            arguments = types.SimpleNamespace(candidates=candidate, sample=sample, previous=[], output=first,
                                              max_groups=1, delay_seconds=1)
            with contextlib.redirect_stdout(io.StringIO()), patch.object(backfill.time, 'sleep'):
                self.assertEqual(backfill.run(arguments, client), 1)
                arguments.previous, arguments.output = [first], second
                arguments.max_groups = 100
                self.assertEqual(backfill.run(arguments, client), 0)
            statuses, unknown = analysis.read_statuses([first, second], analysis.event_records(records),
                                                        study.digest_file(candidate),
                                                        set(analysis.event_records(sampled)), study.digest_file(sample))
            self.assertEqual(len(statuses), 100)
            self.assertEqual(sum(statuses.values()), 48)
            self.assertEqual(unknown, set())
            with self.assertRaisesRegex(ValueError, 'overwrite'):
                backfill.run(arguments, client)

    def test_incomplete_earlier_group_causes_nonzero_even_if_last_succeeds(self):
        original_timeout = socket.getdefaulttimeout()
        self.addCleanup(socket.setdefaulttimeout, original_timeout)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            records, _, candidate, sample = self.candidate_fixture(root)
            order = backfill.ordered_groups(set(analysis.event_records(records)), set())
            failed_code, failed_dates = order[0]
            missing = {(failed_code, date) for date in failed_dates}
            client = Client(self.provider_rows([record for record in records
                                                if (record['code'], record['date']) not in missing]))
            arguments = types.SimpleNamespace(candidates=candidate, sample=sample, previous=[], output=root / 'out.jsonl',
                                              max_groups=100, delay_seconds=1)
            with contextlib.redirect_stdout(io.StringIO()), patch.object(backfill.time, 'sleep'):
                self.assertEqual(backfill.run(arguments, client), 1)


if __name__ == '__main__':
    unittest.main()
