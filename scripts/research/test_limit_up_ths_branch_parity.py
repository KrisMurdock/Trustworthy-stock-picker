import unittest

import numpy as np

import limit_up_ths_branch_parity as branch


def record(codes):
    return {'http_status': 200, 'rpc_error': False, 'tool_error': False, 'business_code': 1,
            'codes': codes, 'selected_count': len(codes)}


class BranchParityTests(unittest.TestCase):
    def test_b_intersection_and_continuation_subsets_are_checked(self):
        one, two, three = '000001.SZ', '000002.SZ', '000003.SZ'
        rows = {name: record([]) for name in branch.NAMES}
        rows.update({'b_parent': record([one, two]), 'b_macd': record([one]), 'b_rsi': record([one]),
                     'b_boll': record([one, two]), 'b_full': record([one]), 'c_parent': record([three]),
                     'c1_parent': record([three]), 'c1_full': record([three])})
        sets, intersection = branch.checked_sets(rows)
        self.assertEqual(intersection, {one})
        self.assertEqual(sets['c1_full'], {three})
        rows['c2_parent'] = record([three])
        with self.assertRaisesRegex(ValueError, 'inconsistent parents'):
            branch.checked_sets(rows)

    def test_comparison_reports_unknown_and_both_mismatch_directions(self):
        symbols = {'000001.SZ', '000002.SZ', '000003.SZ'}
        indexes = {'sz000001': 0, 'sz000002': 1, 'sz000003': 2}
        result = branch.comparison(symbols, indexes, np.array([True, True, False]),
                                   np.array([True, False, True]), {'000002.SZ', '000003.SZ'})
        self.assertEqual(result['compared'], 2)
        self.assertEqual(result['unavailable_or_unknown'], 1)
        self.assertEqual(result['different'], 2)
        self.assertEqual(result['local_only_examples'], ['000001.SZ'])
        self.assertEqual(result['ths_only_examples'], ['000002.SZ'])


if __name__ == '__main__':
    unittest.main()
