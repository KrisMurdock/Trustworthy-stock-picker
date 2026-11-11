import json
import pathlib
import tempfile
import unittest

import numpy as np

import limit_up_volume as volume


class VolumeExperimentTests(unittest.TestCase):
    def test_five_day_volume_average_includes_previous_session(self):
        adjusted = np.tile([10.0, 11.0, 9.0, 10.0, 100.0], (8, 1))
        adjusted[4, 4] = 200.0
        valid = np.ones(8, dtype=bool)
        features = volume.calculate(adjusted, valid)
        self.assertTrue(np.isnan(features['volume_ratio'][3]))
        self.assertAlmostEqual(features['volume_ratio'][4], 200 / 120)
        self.assertAlmostEqual(features['volume_ratio'][5], 100 / 120)

    def test_missing_prices_require_a_fresh_five_day_window(self):
        adjusted = np.tile([10.0, 11.0, 9.0, 10.0, 100.0], (10, 1))
        valid = np.ones(10, dtype=bool)
        valid[4] = False
        features = volume.calculate(adjusted, valid)
        self.assertTrue(np.isnan(features['volume_ratio'][7]))
        self.assertAlmostEqual(features['volume_ratio'][9], 1.0)
        self.assertFalse(features['close_known'][5])

    def test_future_prices_do_not_change_prior_features(self):
        adjusted = np.tile([10.0, 11.0, 9.0, 10.0, 100.0], (10, 1))
        adjusted[:, 3] = np.arange(10, 20)
        valid = np.ones(10, dtype=bool)
        first = volume.calculate(adjusted, valid)
        adjusted[7:, 3] = 500.0
        adjusted[7:, 4] = 100_000.0
        second = volume.calculate(adjusted, valid)
        for name in first:
            np.testing.assert_equal(first[name][:7], second[name][:7])

    def test_filters_exclude_unknown_values_and_partition_known_volume(self):
        features = {'volume_ratio': np.asarray([1.2, 1.0, np.nan, 0.7]),
                    'close_up': np.asarray([True, True, True, False]),
                    'close_known': np.asarray([True, False, True, True])}
        events = {'ma20_above_ma60': np.asarray([True, True, True, False])}
        masks = volume.filter_masks(events, features)
        self.assertEqual(len(masks), 8)
        np.testing.assert_array_equal(masks['volume_gt_ma5'], [True, False, False, False])
        np.testing.assert_array_equal(masks['volume_le_ma5'], [False, True, False, True])
        np.testing.assert_array_equal(masks['prior_close_up'], [True, False, True, False])
        np.testing.assert_array_equal(masks['volume_gt_ma5_and_close_up'], [True, False, False, False])

    def test_replay_rejects_legacy_or_partial_input_before_reading_prices(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            (output / 'extract.json').write_text(json.dumps({
                'event_schema_version': 1, 'limited': False, 'database_sha256': 'not-used',
            }), encoding='utf-8')
            arguments = type('Arguments', (), {'output': output})()
            with self.assertRaisesRegex(ValueError, 'verified'):
                volume.run(arguments)


if __name__ == '__main__':
    unittest.main()
