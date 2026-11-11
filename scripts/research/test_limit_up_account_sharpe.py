import math
import json
import datetime
import pathlib
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import numpy as np

import limit_up_account_sharpe as sharpe
import limit_up_board_analysis as board_analysis
import limit_up_exits as exits
import limit_up_study as study
from test_limit_up_study import price_fixture, set_bar


class LimitUpAccountSharpeTests(unittest.TestCase):
    def test_result_write_is_atomic_and_does_not_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            result = directory / 'result.json'
            with self.assertRaises(ValueError):
                sharpe.write_result(result, {'invalid': float('nan')})
            self.assertFalse(result.exists())
            self.assertEqual(list(directory.iterdir()), [])
            with mock.patch.object(sharpe.os, 'link', side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):
                    sharpe.write_result(result, {'valid': 1})
            self.assertFalse(result.exists())
            self.assertEqual(list(directory.iterdir()), [])
            sharpe.write_result(result, {'valid': 1})
            before = result.read_bytes()
            with self.assertRaises(FileExistsError):
                sharpe.write_result(result, {'valid': 2})
            self.assertEqual(result.read_bytes(), before)
            self.assertEqual(list(directory.iterdir()), [result])

    def test_daily_equity_sharpe_matches_swing_convention(self):
        daily = [{'equity': 100., 'cash': 100., 'positions': 0},
                 {'equity': 110., 'cash': 10., 'positions': 1},
                 {'equity': 99., 'cash': 9., 'positions': 1}]
        result = sharpe.account_metrics(daily, 100., 0.02)
        rate = math.expm1(math.log1p(0.02) / 252)
        self.assertAlmostEqual(result['annual_volatility'], 0.1 * math.sqrt(252))
        self.assertAlmostEqual(result['sharpe'], -rate / 0.1 * math.sqrt(252))
        self.assertAlmostEqual(result['max_drawdown'], 0.1)

    def test_zero_volatility_and_invalid_equity(self):
        daily = [{'equity': 100., 'cash': 100., 'positions': 0}] * 3
        self.assertIsNone(sharpe.account_metrics(daily, 100., 0.02)['sharpe'])
        for invalid in ([], [{'equity': float('nan'), 'cash': 0., 'positions': 0}]):
            with self.assertRaises(ValueError):
                sharpe.account_metrics(invalid, 100., 0.02)

    def test_premarket_watch_does_not_read_same_day_high_low_or_close(self):
        dates, raw, adjusted = price_fixture()
        dates = np.array([f'2021-{1 + index // 28:02}-{1 + index % 28:02}' for index in range(100)])
        for index in range(60):
            price = 8 + index * 0.03
            set_bar(raw, adjusted, index, price, price + 0.2, price - 0.2, price)
        prior_close = raw[59, 3] / 1_000_000
        set_bar(raw, adjusted, 60, round(prior_close * 1.04, 2), round(prior_close * 1.2, 2),
                round(prior_close * 1.01, 2), round(prior_close * 1.1, 2))
        first, _, _, _ = sharpe.premarket_watches('sz300001', dates, raw, adjusted)
        first = [(index, item) for index, item in first if index == 60]
        self.assertEqual(len(first), 1)
        set_bar(raw, adjusted, 60, round(prior_close * 1.04, 2), round(prior_close * 1.10, 2),
                round(prior_close * 1.01, 2), round(prior_close * 1.02, 2))
        second, _, _, _ = sharpe.premarket_watches('sz300001', dates, raw, adjusted)
        self.assertEqual(first, [(index, item) for index, item in second if index == 60])

    def test_no_hindsight_reservations_and_realized_costs(self):
        dates = np.array(['2021-01-04', '2021-01-05', '2021-01-06'])
        watches = {0: [sharpe.Watch('a', 10., 'first_touch', True),
                       sharpe.Watch('b', 20., 'first_touch', True)],
                   1: [sharpe.Watch('b', 20., 'first_touch', True)]}
        entries = {(0, 'b'): sharpe.Entry('b', 0, False, 10., 1, 11., 1, 11.),
                   (1, 'b'): sharpe.Entry('b', 1, False, 10., 2, 11., 2, 11.)}
        closes = {'b': np.array([10., 10., 11.])}
        result, daily, trades = sharpe.simulate_account(
            dates, watches, entries, closes, 0, 2, 'first_touch', True,
            initial=100., cap=100., max_positions=1, annual_cash_yield=0., annual_risk_free=0.)
        self.assertEqual(result['counters']['unfilled_no_touch'], 1)
        self.assertEqual(result['closed_trades'], 1)
        self.assertEqual(trades[0]['entry'], 1)
        self.assertAlmostEqual(trades[0]['pnl'] / 100., 1.1 * 0.998 / 1.002 - 1 - 0.002)
        self.assertAlmostEqual(result['final_equity'], 100. + trades[0]['pnl'])
        self.assertEqual(daily[0]['positions'], 0)

    def test_cash_interest_before_orders_and_duplicate_position(self):
        dates = np.array(['2021-01-04', '2021-01-05', '2021-01-06'])
        watches = {0: [sharpe.Watch('a', 10., 'first_touch', True)],
                   1: [sharpe.Watch('a', 10., 'first_touch', True)]}
        entries = {(0, 'a'): sharpe.Entry('a', 0, False, 10., 2, 10., 2, 10.),
                   (1, 'a'): sharpe.Entry('a', 1, False, 10., 2, 10., 2, 10.)}
        closes = {'a': np.array([10., 10., 10.])}
        result, daily, trades = sharpe.simulate_account(
            dates, watches, entries, closes, 0, 2, 'first_touch', True,
            initial=1000., cap=100., max_positions=2, slippage=0., fee=0.)
        rate = math.expm1(math.log1p(0.02) / 252)
        self.assertAlmostEqual(daily[0]['cash'], 1000 * (1 + rate) - 100)
        self.assertAlmostEqual(daily[1]['cash'], daily[0]['cash'] * (1 + rate))
        self.assertEqual(result['counters']['skipped_duplicate'], 1)
        self.assertEqual(len(trades), 1)

    def test_unknown_or_stale_data_cannot_report_valid_sharpe(self):
        dates = np.array(['2021-01-04', '2021-01-05'])
        watches = {0: [sharpe.Watch('a', 10., 'first_touch', True)]}
        entries = {(0, 'a'): sharpe.Entry('a', 0, False, 10., None, None, None, None)}
        result, _, _ = sharpe.simulate_account(dates, watches, entries, {'a': np.array([10., np.nan])},
                                                0, 1, 'first_touch', True)
        self.assertFalse(result['data_complete'])
        self.assertIsNone(result['sharpe'])
        self.assertEqual(result['counters']['unresolved_exit_entries'], 1)
        self.assertEqual(result['counters']['stale_position_days'], 1)
        self.assertEqual(result['stale_valuations'], [{'date': '2021-01-05', 'code': 'a'}])
        self.assertEqual(result['unresolved_entries'], [{'date': '2021-01-04', 'code': 'a'}])

    def test_market_replay_keeps_watches_for_stocks_that_never_touch(self):
        dates, raw, adjusted = price_fixture()
        dates = np.asarray([(datetime.date(2021, 1, 1) + datetime.timedelta(days=index)).isoformat()
                            for index in range(len(dates))])
        for index in range(60):
            price = 8 + index * 0.03
            set_bar(raw, adjusted, index, price, price + 0.2, price - 0.2, price)
        prior = raw[59, 3] / 1_000_000
        set_bar(raw, adjusted, 60, round(prior * 1.04, 2), round(prior * 1.2, 2),
                round(prior * 1.01, 2), round(prior * 1.1, 2))
        raw_other, adjusted_other = raw.copy(), adjusted.copy()
        set_bar(raw_other, adjusted_other, 60, round(prior * 1.04, 2), round(prior * 1.1, 2),
                round(prior * 1.01, 2), round(prior * 1.08, 2))
        raw_unknown, adjusted_unknown = raw_other.copy(), adjusted_other.copy()
        raw_unknown[60, 1] = 0
        adjusted_unknown[60, 1] = np.nan
        events, _ = study.extract_stock('sz300001', dates, raw, adjusted)
        selected = study.select_mask(events, sharpe.minute_sample.BASE_CONFIG)
        events = {name: values[selected] for name, values in events.items()}
        self.assertEqual(len(events['date']), 1)
        key = ('sz300001', str(dates[60]))
        records = {key: {'daily_cents': (raw[60, :4] // 10000).tolist()}}
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            database = directory / 'market.db'
            connection = sqlite3.connect(database)
            connection.execute('CREATE TABLE stock (code TEXT, active INTEGER)')
            connection.executemany('INSERT INTO stock VALUES (?, 1)',
                                   [('sz300001',), ('sz300002',), ('sz300003',)])
            connection.commit()
            connection.close()
            for code in ('sz300001', 'sz300002', 'sz300003'):
                (directory / (code + '.json.gz')).touch()
            arguments = SimpleNamespace(database=database, qfq_dir=directory, manifest=directory / 'manifest')
            prices = {'sz300001': (raw, adjusted), 'sz300002': (raw_other, adjusted_other),
                      'sz300003': (raw_unknown, adjusted_unknown)}
            manifest = {code + '.json.gz': 'verified' for code in prices}
            with mock.patch.object(study, 'load_manifest', return_value=manifest), \
                    mock.patch.object(study, 'read_raw', side_effect=lambda _, code, __: prices[code][0]), \
                    mock.patch.object(study, 'read_adjusted', side_effect=lambda _, code, *args: prices[code][1]):
                watches, entries, _, _, _, _, unknown, coverage = sharpe.replay_market(
                    arguments, events, dates, records, {key: False})
            self.assertEqual({item.code for item in watches[60]}, set(prices))
            self.assertEqual(set(entries), {(60, 'sz300001')})
            self.assertEqual(coverage['scanned_growth_stocks'], 3)
            self.assertIn((60, 'sz300003'), unknown)

    def test_unknown_touch_is_not_treated_as_a_confirmed_no_fill(self):
        dates = np.asarray(['2021-01-04', '2021-01-05'])
        watches = {0: [sharpe.Watch('a', 1., 'first_touch', True),
                       sharpe.Watch('b', 2., 'first_touch', True)]}
        entries = {(0, 'b'): sharpe.Entry('b', 0, False, 10., 1, 11., 1, 11.)}
        result, _, trades = sharpe.simulate_account(
            dates, watches, entries, {'b': np.asarray([10., 11.])}, 0, 1, 'first_touch', True,
            initial=100., cap=100., max_positions=1, unknown_touches={(0, 'a')})
        self.assertEqual(trades, [])
        self.assertEqual(result['counters']['unknown_touch'], 1)
        self.assertEqual(result['counters']['unfilled_no_touch'], 0)
        self.assertEqual(result['unknown_touches'], [{'date': '2021-01-04', 'code': 'a'}])
        self.assertFalse(result['data_complete'])
        self.assertIsNone(result['sharpe'])

    def test_frozen_inputs_reject_tampering_and_reference_mismatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            output = directory / 'events'
            output.mkdir()
            files = {name: directory / name for name in ('database', 'calendar', 'manifest')}
            files['events'] = output / 'events.npz'
            for name, path in files.items():
                path.write_bytes(name.encode())
            metadata = {'event_schema_version': study.EVENT_SCHEMA_VERSION, 'limited': False,
                        **{name + '_sha256': study.digest_file(path) for name, path in files.items()}}
            (output / 'extract.json').write_text(json.dumps(metadata), encoding='utf-8')
            evidence = {'candidate_sha256': 'candidate', 'sample_sha256': 'sample',
                        'status_sha256': ['status'], 'reference_sha256': 'reference'}
            reference = directory / 'board.json'
            reference.write_text(json.dumps({'events_sha256': metadata['events_sha256'],
                                              'database_sha256': metadata['database_sha256'],
                                              'evidence': evidence,
                                              'holdout_signals_examined': False}), encoding='utf-8')
            arguments = SimpleNamespace(database=files['database'], calendar=files['calendar'],
                                        manifest=files['manifest'], output=output,
                                        board_reference=reference, result=directory / 'result.json')
            with mock.patch.object(sharpe.win_analysis, 'load_evidence', return_value=({}, {}, {}, evidence)):
                sharpe.verify_frozen_inputs(arguments)
                files['events'].write_bytes(b'tampered')
                with self.assertRaisesRegex(ValueError, 'hash'):
                    sharpe.verify_frozen_inputs(arguments)
                files['events'].write_bytes(b'events')
                reference.write_text(reference.read_text().replace('candidate', 'different'), encoding='utf-8')
                with self.assertRaisesRegex(ValueError, 'reference'):
                    sharpe.verify_frozen_inputs(arguments)
                with mock.patch.object(study, 'digest_file', side_effect=PermissionError('denied')):
                    with self.assertRaises(PermissionError):
                        sharpe.verify_frozen_inputs(arguments)

    def test_entry_matches_frozen_path_and_delayed_exit(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.40, 12.00, 10.30, 11.00)
        set_bar(raw, adjusted, 63, 10.45, 10.60, 10.30, 10.40)
        set_bar(raw, adjusted, 64, 10.30, 10.50, 10.20, 10.40)
        events, _ = study.extract_stock('sz300001', dates, raw, adjusted)
        index = int(np.flatnonzero(events['date'] == dates[62])[0])
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 20, view)
        ratio = events['sale_ratio'][index, 0]
        delay = int(events['delay'][index, 0])
        entry, holding_ratio, holding_delay = sharpe.replay_entry(
            'sz300001', 62, view, known, sealed, False, ratio, delay)
        self.assertEqual(entry.fixed_exit, 64)
        self.assertEqual(entry.holding_exit, 64)
        self.assertEqual(holding_delay, 1)
        self.assertAlmostEqual(entry.fixed_sale / entry.price, ratio)
        self.assertAlmostEqual(holding_ratio, ratio)
        with self.assertRaisesRegex(ValueError, 'delay'):
            sharpe.replay_entry('sz300001', 62, view, known, sealed, False, ratio, 0)
        with self.assertRaisesRegex(ValueError, 'ratio'):
            sharpe.replay_entry('sz300001', 62, view, known, sealed, False, ratio + 0.01, delay)

    def test_unavailable_exit_is_not_invented(self):
        dates, raw, adjusted = price_fixture()
        set_bar(raw, adjusted, 62, 10.40, 12.00, 10.30, 11.00)
        raw[63:] = 0
        adjusted[63:] = np.nan
        view = exits.price_view(raw, adjusted)
        known, sealed = board_analysis.board_states(raw, adjusted, 20, view)
        entry, ratio, delay = sharpe.replay_entry('sz300001', 62, view, known, sealed, False, np.nan, -1)
        self.assertIsNone(entry.fixed_exit)
        self.assertIsNone(entry.holding_exit)
        self.assertTrue(np.isnan(ratio))
        self.assertEqual(delay, -1)

    def test_reservations_never_overallocate_and_st_is_not_reallocated(self):
        dates = np.asarray(['2021-01-04', '2021-01-05'])
        watches = {0: [sharpe.Watch('a', 1., 'first_touch', True),
                       sharpe.Watch('b', 2., 'first_touch', True),
                       sharpe.Watch('c', 3., 'first_touch', True)]}
        entries = {(0, 'a'): sharpe.Entry('a', 0, True, 10., 1, 11., 1, 11.),
                   (0, 'b'): sharpe.Entry('b', 0, False, 10., 1, 11., 1, 11.),
                   (0, 'c'): sharpe.Entry('c', 0, False, 10., 1, 11., 1, 11.)}
        closes = {code: np.asarray([10., 11.]) for code in ('a', 'b', 'c')}
        result, daily, trades = sharpe.simulate_account(dates, watches, entries, closes, 0, 1,
                                                        'first_touch', True, initial=100., cap=60.,
                                                        max_positions=2, slippage=0., fee=0.,
                                                        annual_cash_yield=0., annual_risk_free=0.)
        self.assertEqual(result['counters']['unfilled_st'], 1)
        self.assertEqual(result['counters']['skipped_capacity'], 1)
        self.assertEqual([trade['code'] for trade in trades], ['b'])
        self.assertEqual(trades[0]['budget'], 40.)
        self.assertEqual(daily[0]['cash'], 60.)

    def test_reference_summary_rejects_changed_metrics(self):
        actual = {'signals': 3, 'closed': 2, 'unresolved': 1, 'win_rate': 0.5, 'pf': 1.25}
        sharpe.verify_summary(actual, actual.copy())
        with self.assertRaisesRegex(ValueError, 'pf'):
            sharpe.verify_summary(actual, {**actual, 'pf': 1.26})


if __name__ == '__main__':
    unittest.main()
