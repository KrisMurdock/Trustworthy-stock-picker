import unittest

import limit_up_ths_parity as parity


def record(codes, count=None):
    return {'http_status': 200, 'rpc_error': False, 'tool_error': False, 'business_code': 1,
            'codes': codes, 'selected_count': len(codes) if count is None else count}


class ThsParityTests(unittest.TestCase):
    def test_truncated_response_is_not_treated_as_complete(self):
        with self.assertRaisesRegex(ValueError, 'truncated'):
            parity.checked_codes(record(['000001.SZ'], 101))

    def test_business_failure_and_duplicate_codes_are_rejected(self):
        failure = record([])
        failure['tool_error'] = True
        with self.assertRaisesRegex(ValueError, 'did not succeed'):
            parity.checked_codes(failure)
        with self.assertRaisesRegex(ValueError, 'invalid'):
            parity.checked_codes(record(['000001.SZ', '000001.SZ']))

    def test_unauthorized_and_forbidden_responses_fail_closed(self):
        for status in (401, 403):
            failure = record(['000001.SZ'])
            failure['http_status'] = status
            with self.assertRaisesRegex(ValueError, 'did not succeed'):
                parity.checked_codes(failure)

    def test_combined_selection_compares_to_all_components(self):
        one, two = '000001.SZ', '000002.SZ'
        rows = {name: record([one, two]) for name in parity.NAMES}
        rows['macd_growth'] = record([one])
        rows['full'] = record([one])
        _, intersection = parity.selection_sets(rows)
        self.assertEqual(intersection, {one})
        rows['upper'] = record(['000003.SZ'])
        with self.assertRaisesRegex(ValueError, 'outside'):
            parity.selection_sets(rows)

    def test_empty_result_does_not_confirm_indicator_dates(self):
        self.assertFalse(parity.verify_dates(record([]), ['2021-07-29']))
        response = record(['000001.SZ'])
        response['business_data'] = {'answer': '| 股票代码 | macd(macd值)[20210729] |'}
        self.assertTrue(parity.verify_dates(response, ['2021-07-29']))
        with self.assertRaisesRegex(ValueError, 'incorrect indicator dates'):
            parity.verify_dates(response, ['2021-07-28'])

    def test_date_must_belong_to_the_correct_indicator_field(self):
        response = record(['000001.SZ'])
        response['business_data'] = {'answer': '| 股票代码 | macd(macd值)[20210729] | rsi(rsi6值)[20210728] |'}
        with self.assertRaisesRegex(ValueError, 'incorrectly dated fields'):
            parity.verify_dates(response, ['2021-07-28'], [('macd(macd值)', '2021-07-28')])
        self.assertTrue(parity.verify_dates(response, ['2021-07-28'], [('rsi(rsi6值)', '2021-07-28')]))

    def test_only_development_period_dates_are_accepted(self):
        dates = {'2020-12-31': 100, '2021-01-04': 101, '2025-01-02': 102}
        self.assertEqual(parity.development_position('2021-01-04', dates), 101)
        for date in ('2020-12-31', '2025-01-02', '2024-01-06'):
            with self.assertRaisesRegex(ValueError, 'not a development'):
                parity.development_position(date, dates)

    def test_comparison_reports_both_mismatch_directions(self):
        result = parity.compare({'one': True, 'two': False}, {'one': False, 'two': True})
        self.assertEqual(result['different'], 2)
        self.assertEqual(result['local_only_examples'], ['one'])
        self.assertEqual(result['ths_only_examples'], ['two'])
        with self.assertRaisesRegex(ValueError, 'sets differ'):
            parity.compare({'one': True}, {})


if __name__ == '__main__':
    unittest.main()
