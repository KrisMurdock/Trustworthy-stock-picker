import unittest

import numpy as np

import limit_up_swing_compare as compare


class MatchedSwingTests(unittest.TestCase):
    def test_excludes_missing_growth_prices_and_main_board(self):
        stocks = ['sh600000', 'sz300001', 'sh688001', 'sh688002']
        mask = compare.matching_universe(stocks, stocks, ['sh688002'])
        np.testing.assert_array_equal(mask, [False, True, True, False])

    def test_missing_or_duplicate_stocks_reject_the_comparison(self):
        for stocks, active, missing in ((['sz300001'], ['sz300001', 'sh688001'], []),
                                        (['sz300001', 'sz300001'], ['sz300001'], []),
                                        (['sz300001'], ['sz300001'], ['sh688001'])):
            with self.assertRaises(ValueError):
                compare.matching_universe(stocks, active, missing)

    def test_period_uses_entry_date_including_previous_session_signal(self):
        np.testing.assert_array_equal(compare.entry_period([98, 99, 100, 101, 102], 100, 102),
                                      [False, True, True, True, False])
        with self.assertRaises(ValueError):
            compare.entry_period([0], 0, 2)

    def test_frozen_wave_data_and_policy_order_must_match(self):
        limit = {'inputs': {'database_sha256': 'db', 'calendar_sha256': 'cal', 'manifest_sha256': 'manifest'}}
        expanded = {'limited': False, 'input_hashes': {'database': 'db', 'calendar': 'cal'},
                    'prices_manifest_sha256': 'manifest', 'policies': ['policy']}
        atr = {'limited': False, 'source_paths_sha256': 'paths', 'atr_data_sha256': 'atr'}
        archive = {'sha256': {'paths': 'paths', 'atr': 'atr'}}
        compare.verify_wave_alignment(limit, expanded, atr, archive, ['policy'])
        with self.assertRaises(ValueError):
            compare.verify_wave_alignment(limit, {**expanded, 'prices_manifest_sha256': 'wrong'},
                                          atr, archive, ['policy'])
        with self.assertRaises(ValueError):
            compare.verify_wave_alignment(limit, expanded, {**atr, 'source_paths_sha256': 'wrong'},
                                          archive, ['policy'])
        with self.assertRaises(ValueError):
            compare.verify_wave_alignment(limit, expanded, atr, archive, ['other'])

    def test_matching_comparison_rejects_different_account_assumptions(self):
        assumptions = {'initial': 200000, 'position_cap': 20000, 'max_positions': 10,
                       'annual_risk_free': 0.02, 'round_trip_fee_on_budget': 0.002}
        compare.verify_limit_assumptions({'assumptions': assumptions})
        for field, changed in (('initial', 100000), ('position_cap', 10000), ('max_positions', 5),
                               ('annual_risk_free', 0.), ('round_trip_fee_on_budget', 0.001)):
            with self.assertRaises(ValueError):
                compare.verify_limit_assumptions({'assumptions': {**assumptions, field: changed}})


if __name__ == '__main__':
    unittest.main()
