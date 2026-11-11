import unittest

import numpy as np

import limit_up_main_board as main_board
import limit_up_study as study


class MainBoardResearchTests(unittest.TestCase):
    def test_universe_uses_only_development_signal_dates_and_main_board(self):
        events = {'date': np.array(['2021-01-04', '2024-12-31', '2025-01-01', '2020-12-31',
                                    '2022-01-01', '2022-01-02', '2022-01-03']),
                  'board': np.array([10, 10, 10, 10, 20, 10, 10]),
                  'ma20_above_ma60': np.array([True, True, True, True, True, False, True]),
                  'gap': np.array([0.0, 0.069, 0.01, 0.01, 0.01, 0.01, 0.07])}
        self.assertEqual(main_board.development_indices(events).tolist(), [0, 1])

    def test_supported_filters_and_missing_values_are_explicit(self):
        events = {'date': np.array(['2021-01-04'] * 4),
                  'above_ma20': np.array([True, True, False, True])}
        features = {'dmi_difference': np.array([0.1, -0.1, np.nan, 0.1]),
                    'volume_ratio': np.array([1.1, 1.0, 1.2, np.nan]),
                    'close_known': np.array([True, True, False, True]),
                    'close_up': np.array([True, False, True, True])}
        masks = main_board.filter_masks(events, features)
        self.assertEqual(tuple(masks), main_board.FILTER_NAMES)
        self.assertEqual(masks['pdi_gt_mdi'][0].tolist(), [True, False, False, True])
        self.assertEqual(masks['pdi_and_volume_gt_ma5'][0].tolist(), [True, False, False, False])
        self.assertEqual(masks['pdi_and_volume_gt_ma5'][1].tolist(), [True, True, False, False])
        self.assertEqual(masks['pdi_and_volume_le_ma5'][0].tolist(), [False, False, False, False])
        self.assertEqual(masks['pdi_and_close_up'][0].tolist(), [True, False, False, True])
        self.assertEqual(masks['pdi_and_close_above_ma20'][0].tolist(), [True, False, False, True])

    def test_all_four_years_and_both_uncertainty_intervals_are_required(self):
        stats = {'closed': 30, 'days': 20, 'stocks': 20, 'unresolved': 0, 'pf': 1.01}
        periods = {f'year_{year}': {'20bp': dict(stats)} for year in range(2021, 2025)}
        periods.update({name: {'20bp_day_cluster_ci': {'pf_95': [1.01, 2]}}
                        for name in ('training_2021_2023', 'selection_2024')})
        self.assertTrue(main_board.annual_and_uncertainty_gate(periods))
        periods['year_2022']['20bp']['pf'] = 1.0
        self.assertFalse(main_board.annual_and_uncertainty_gate(periods))
        periods['year_2022']['20bp']['pf'] = 1.01
        periods['selection_2024']['20bp_day_cluster_ci'] = {'pf_95': [1, 2]}
        self.assertFalse(main_board.annual_and_uncertainty_gate(periods))
        periods['selection_2024']['20bp_day_cluster_ci'] = None
        self.assertFalse(main_board.annual_and_uncertainty_gate(periods))

    def test_small_sample_never_passes_the_36_combination_grid(self):
        events = {'code': np.array(['sz000001', 'sh600002']),
                  'date': np.array(['2021-01-04', '2024-12-31']),
                  'gap': np.array([0.01, 0.04]), 'above_ma20': np.array([True, True]),
                  'sale_ratio': np.array([[1.2] * len(study.HOLDS), [0.9] * len(study.HOLDS)]),
                  'delay': np.zeros((2, len(study.HOLDS)), dtype=np.int16),
                  'closed_at_limit': np.array([True, False])}
        features = {'dmi_difference': np.array([0.1, 0.1]), 'volume_ratio': np.array([1.1, 0.9]),
                    'close_known': np.array([True, True]), 'close_up': np.array([True, True])}
        results = main_board.evaluate(events, features, np.array([2, 3]))
        self.assertEqual(len(results), 36)
        self.assertTrue(all(not record['strict_development_passed'] for record in results))
        self.assertTrue(all(not record['sample_ok'] for record in results))


if __name__ == '__main__':
    unittest.main()
