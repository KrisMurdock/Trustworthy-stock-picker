import argparse
import pathlib
import tempfile
import unittest

import numpy as np

import limit_up_momentum_boll as momentum
import limit_up_momentum_diagnostics as diagnostics


class MomentumDiagnosticsTests(unittest.TestCase):
    def test_sample_deviation_matches_direct_calculation_and_preserves_missing(self):
        values = np.arange(1., 23.)
        middle, upper, lower = momentum.bollinger(values, np.ones(len(values), dtype=bool))
        actual = diagnostics.sample_deviation_features({'middle': middle, 'upper': upper, 'lower': lower})
        expected = momentum.bollinger(values, np.ones(len(values), dtype=bool), ddof=1)
        np.testing.assert_allclose(actual['upper'], expected[1], equal_nan=True)
        np.testing.assert_allclose(actual['lower'], expected[2], equal_nan=True)
        with self.assertRaises(ValueError):
            diagnostics.sample_deviation_features({'middle': middle, 'upper': upper[:-1], 'lower': lower})

    def test_post_entry_groups_account_for_delayed_and_unresolved(self):
        events = {'date': np.array(['2021-01-04'] * 4), 'code': np.array(['a', 'b', 'c', 'd']),
                  'closed_at_limit': np.array([True, False, True, False]),
                  'sale_ratio': np.array([[0.9, 1, 1, 1], [1.1, 1, 1, 1],
                                          [0.8, 1, 1, 1], [np.nan, 1, 1, 1]]),
                  'delay': np.array([[0, 0, 0, 0], [0, 0, 0, 0],
                                     [2, 0, 0, 0], [-1, 0, 0, 0]])}
        result = diagnostics.failure_breakdown(events, np.ones(4, dtype=bool))
        self.assertEqual(result['sealed_at_buy_day_close']['metrics']['signals'], 2)
        self.assertEqual(result['not_sealed_at_buy_day_close']['metrics']['signals'], 2)
        self.assertEqual(result['next_open_below_entry_undelayed']['metrics']['signals'], 1)
        self.assertEqual(result['next_open_at_or_above_entry_undelayed']['metrics']['signals'], 1)
        self.assertEqual(result['delayed_or_missing_data_proxy']['metrics']['signals'], 1)
        self.assertEqual(result['unresolved']['metrics']['signals'], 1)
        self.assertAlmostEqual(result['sealed_at_buy_day_close']['share_of_total_loss'], 1.0)
        events['delay'][3, 0] = 0
        with self.assertRaisesRegex(ValueError, 'inconsistent exit'):
            diagnostics.failure_breakdown(events, np.ones(4, dtype=bool))

    def test_reference_mismatch_fails_closed(self):
        metadata = {name: name for name in ('events_sha256', 'database_sha256',
                                            'calendar_sha256', 'manifest_sha256')}
        reference = {'schema_version': 1, 'holdout_signals_examined': False,
                     **metadata, 'evidence': {'source': 1}, 'results': [1]}
        diagnostics.verify_reference(reference, metadata, {'source': 1}, [1])
        with self.assertRaisesRegex(ValueError, 'differ'):
            diagnostics.verify_reference(reference, metadata, {'source': 1}, [2])
        reference['holdout_signals_examined'] = True
        with self.assertRaisesRegex(ValueError, 'differ'):
            diagnostics.verify_reference(reference, metadata, {'source': 1}, [1])

    def test_existing_diagnostic_output_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            result = pathlib.Path(directory) / 'result.json'
            result.write_text('previous evidence', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'overwrite'):
                diagnostics.run(argparse.Namespace(result=result))
            self.assertEqual(result.read_text(encoding='utf-8'), 'previous evidence')

    def test_grid_target_requires_both_periods_and_sample_gates(self):
        valid = {'closed': 300, 'days': 80, 'stocks': 50, 'unresolved': 0,
                 'pf': 1.6, 'payoff': 1.3, 'mean_return': 0.01}
        selection = {**valid, 'closed': 80, 'days': 40, 'stocks': 25}
        self.assertTrue(diagnostics.eligible_pair(valid, selection))
        self.assertFalse(diagnostics.eligible_pair(valid, {**selection, 'days': 39}))
        self.assertFalse(diagnostics.eligible_pair(valid, {**selection, 'unresolved': 1}))


if __name__ == '__main__':
    unittest.main()
