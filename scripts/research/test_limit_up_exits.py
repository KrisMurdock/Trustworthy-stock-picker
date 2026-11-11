import unittest

import numpy as np

import limit_up_exits as exits


def fixture():
    size = 8
    return {'valid': np.ones(size, dtype=bool), 'normal': np.ones(size, dtype=bool),
            'raw_open': np.full(size, 10000), 'raw_low': np.full(size, 9900),
            'down_five': np.full(size, 9500), 'adj_open': np.full(size, 100.0),
            'adj_high': np.full(size, 101.0), 'adj_low': np.full(size, 99.0)}


class ExitReplayTests(unittest.TestCase):
    def test_both_barriers_hit_uses_stop_first_and_day_low(self):
        view = fixture()
        view['adj_high'][1] = 109
        view['adj_low'][1] = 96
        view['raw_low'][1] = 9600
        ratio, delayed, reason, ambiguous = exits.replay_policy(view, 0, 100, exits.POLICIES[1])
        self.assertEqual((ratio, delayed, reason, ambiguous), (0.96, 0, 'intraday_stop_at_low', True))

    def test_suspected_limit_down_sets_pending_stop_until_next_open(self):
        view = fixture()
        view['adj_low'][1] = 94
        view['raw_low'][1] = 9400
        view['adj_open'][2] = 99
        ratio, delayed, reason, ambiguous = exits.replay_policy(view, 0, 100, exits.POLICIES[2])
        self.assertEqual((ratio, delayed, reason, ambiguous), (0.99, 1, 'delayed_stop', False))

    def test_opening_above_target_uses_target_not_unavailable_opening_upside(self):
        view = fixture()
        view['adj_open'][1] = 112
        ratio, _, reason, _ = exits.replay_policy(view, 0, 100, exits.POLICIES[1])
        self.assertEqual((ratio, reason), (1.08, 'opening_target'))

    def test_opening_below_stop_above_price_floor_sells_at_open(self):
        view = fixture()
        view['adj_open'][1] = 96
        view['raw_open'][1] = 9600
        ratio, _, reason, _ = exits.replay_policy(view, 0, 100, exits.POLICIES[0])
        self.assertEqual((ratio, reason), (0.96, 'opening_stop'))

    def test_blocked_open_does_not_assume_intraday_target_fill(self):
        view = fixture()
        view['raw_open'][1] = 9500
        view['adj_open'][1] = 95
        view['adj_high'][1] = 112
        view['adj_low'][1] = 94
        view['raw_low'][1] = 9400
        view['adj_open'][2] = 99
        ratio, delayed, reason, _ = exits.replay_policy(view, 0, 100, exits.POLICIES[1])
        self.assertEqual((ratio, delayed, reason), (0.99, 1, 'delayed_stop'))

    def test_time_exit_never_sells_on_entry_day(self):
        view = fixture()
        view['adj_high'][0] = 120
        view['adj_low'][0] = 80
        view['adj_open'][3] = 102
        ratio, delayed, reason, _ = exits.replay_policy(view, 0, 100, exits.POLICIES[1])
        self.assertEqual((ratio, delayed, reason), (1.02, 0, 'expiry'))

    def test_missing_future_prices_stay_unresolved(self):
        view = fixture()
        view['valid'][1:] = False
        ratio, delayed, reason, _ = exits.replay_policy(view, 0, 100, exits.POLICIES[1])
        self.assertTrue(np.isnan(ratio))
        self.assertEqual((delayed, reason), (1, 'unresolved'))


if __name__ == '__main__':
    unittest.main()
