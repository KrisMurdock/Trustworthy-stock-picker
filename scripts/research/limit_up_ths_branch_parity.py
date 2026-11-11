#!/usr/bin/env python3
"""Compare weak-to-strong and continuation branches with historical THS results."""

import argparse
import collections
import json
import pathlib

import numpy as np

import limit_up_account_sharpe as account
import limit_up_momentum_boll as momentum
import limit_up_momentum_diagnostics as diagnostics
import limit_up_study as study
import limit_up_ths_parity as parity


NAMES = ('b_parent', 'b_macd', 'b_rsi', 'b_boll', 'b_full', 'c_parent',
         'c1_parent', 'c2_parent', 'c1_full', 'c2_full')


def checked_sets(records):
    if set(records) != set(NAMES):
        raise ValueError('missing or unexpected THS branch query')
    sets = {name: parity.checked_codes(records[name]) for name in NAMES}
    if (any(not sets[name] <= sets['b_parent'] for name in ('b_macd', 'b_rsi', 'b_boll', 'b_full'))
            or not sets['c1_parent'] <= sets['c_parent']
            or not sets['c2_parent'] <= sets['c_parent']
            or sets['c1_parent'] & sets['c2_parent']
            or not sets['c1_full'] <= sets['c1_parent']
            or not sets['c2_full'] <= sets['c2_parent']):
        raise ValueError('THS branch sets have inconsistent parents')
    intersection = sets['b_macd'] & sets['b_rsi'] & sets['b_boll']
    return sets, intersection


def local_symbol(symbol):
    number, exchange = symbol.split('.')
    return exchange.lower() + number


def required_fields(name, date, previous, earlier):
    base = parity.parent_fields(date, previous)
    macd = [('macd(macd值)', previous), ('macd(macd值)', earlier)]
    rsi = [('rsi(rsi6值)', previous), ('rsi(rsi6值)', earlier)]
    boll = [('收盘价:前复权', previous), ('boll(lower值)', previous), ('boll(mid值)', previous)]
    earlier_board = [('收盘价:不复权', earlier), ('涨停价', earlier)]
    follow = [('macd(macd值)', previous), ('rsi(rsi6值)', previous),
              ('收盘价:前复权', previous), ('boll(mid值)', previous)]
    extras = {'b_parent': [], 'b_macd': macd, 'b_rsi': rsi, 'b_boll': boll,
              'b_full': macd + rsi + boll, 'c_parent': [], 'c1_parent': earlier_board,
              'c2_parent': earlier_board, 'c1_full': earlier_board + follow,
              'c2_full': earlier_board + follow}
    return base + extras[name]


def comparison(symbols, indexes, known, selected, remote):
    local = {symbol: bool(selected[indexes[local_symbol(symbol)]]) for symbol in symbols
             if local_symbol(symbol) in indexes and known[indexes[local_symbol(symbol)]]}
    return {**parity.compare(local, {symbol: symbol in remote for symbol in local}),
            'unavailable_or_unknown': len(symbols) - len(local)}


def run(arguments):
    if arguments.result.exists():
        raise ValueError('refusing to overwrite branch-parity result')
    metadata = json.loads((arguments.input / 'extract.json').read_text(encoding='utf-8'))
    checks = ((arguments.database, metadata.get('database_sha256')),
              (arguments.calendar, metadata.get('calendar_sha256')),
              (arguments.manifest, metadata.get('manifest_sha256')),
              (arguments.input / 'events.npz', metadata.get('events_sha256')))
    if metadata.get('limited') or any(not digest or study.digest_file(path) != digest for path, digest in checks):
        raise ValueError('frozen input hash mismatch')
    observations = json.loads(arguments.observations.read_text(encoding='utf-8'))
    groups = collections.defaultdict(dict)
    for record in observations.get('records', []):
        date, name = record.get('date'), record.get('name')
        if not isinstance(date, str) or name in groups[date]:
            raise ValueError('duplicate or invalid THS observation')
        groups[date][name] = record
    if not groups:
        raise ValueError('no THS branch observations')
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if len(dates) < 100 or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('invalid frozen trading calendar')
    date_index = {str(date): index for index, date in enumerate(dates)}
    remote = {}
    pairs = set()
    results = {}
    for date, records in sorted(groups.items()):
        position = parity.development_position(date, date_index)
        previous, earlier = str(dates[position - 1]), str(dates[position - 2])
        sets, intersection = checked_sets(records)
        unverified = []
        for name in NAMES:
            required = [date, previous]
            if name in ('b_macd', 'b_rsi', 'b_full', 'c1_parent', 'c2_parent', 'c1_full', 'c2_full'):
                required.append(earlier)
            if not parity.verify_dates(records[name], required, required_fields(name, date, previous, earlier)):
                unverified.append(name)
        remote[date] = sets
        pairs.update((local_symbol(symbol), date) for symbol in sets['b_parent'] | sets['c_parent'])
        results[date] = {'ths_counts': {name: len(sets[name]) for name in NAMES},
                         'b_combined_difference': len(intersection ^ sets['b_full']),
                         'empty_result_dates_unverified': unverified, 'comparisons': {}}
    all_events = study.load_events(arguments.input / 'events.npz')
    selected = np.asarray([(str(code), str(date)) in pairs
                           for code, date in zip(all_events['code'], all_events['date'])])
    events = {name: values[selected] for name, values in all_events.items()}
    event_pairs = [(str(code), str(date)) for code, date in zip(events['code'], events['date'])]
    if len(event_pairs) != len(set(event_pairs)):
        raise ValueError('duplicate local touch event')
    features = diagnostics.sample_deviation_features(
        momentum.extract_features(arguments, events, dates, study.load_manifest(arguments.manifest)))
    macd = features['macd']
    earlier_macd = features['earlier_macd']
    rsi, earlier_rsi = features['rsi'], features['earlier_rsi']
    close, middle, lower = features['close'], features['middle'], features['lower']
    previous_known, previous_sealed = features['previous_known'], features['previous_sealed']
    earlier_known, earlier_sealed = features['earlier_known'], features['earlier_sealed']
    macd_known = np.isfinite(macd) & np.isfinite(earlier_macd)
    rsi_known = np.isfinite(rsi) & np.isfinite(earlier_rsi)
    boll_known = np.isfinite(close) & np.isfinite(middle) & np.isfinite(lower)
    b_parent = previous_known & ~previous_sealed
    c_parent = previous_known & previous_sealed
    b_macd = (macd < 0) & (macd > earlier_macd)
    b_rsi = (rsi >= 30) & (rsi < 55) & (rsi > earlier_rsi)
    b_boll = (close >= lower) & (close < middle)
    c_known = np.isfinite(macd) & np.isfinite(rsi) & np.isfinite(close) & np.isfinite(middle)
    c_filter = (macd > 0) & (rsi >= 55) & (rsi < 85) & (close >= middle)
    for date in results:
        indexes = {str(code): index for index, (code, event_date) in enumerate(event_pairs) if event_date == date}
        sets = remote[date]
        comparisons = results[date]['comparisons']
        comparisons['b_parent_state'] = comparison(sets['b_parent'], indexes, previous_known, b_parent,
                                                    sets['b_parent'])
        comparisons['c_parent_state'] = comparison(sets['c_parent'], indexes, previous_known, c_parent,
                                                    sets['c_parent'])
        for name, known, value in (('b_macd', macd_known, b_macd), ('b_rsi', rsi_known, b_rsi),
                                   ('b_boll', boll_known, b_boll),
                                   ('b_full', macd_known & rsi_known & boll_known, b_macd & b_rsi & b_boll)):
            comparisons[name] = comparison(sets['b_parent'], indexes, b_parent & known, value, sets[name])
        for name, board_state in (('c1', ~earlier_sealed), ('c2', earlier_sealed)):
            comparisons[name + '_parent'] = comparison(sets['c_parent'], indexes, c_parent & earlier_known,
                                                        board_state, sets[name + '_parent'])
            comparisons[name + '_full'] = comparison(sets[name + '_parent'], indexes,
                                                      c_parent & earlier_known & c_known & board_state,
                                                      c_filter, sets[name + '_full'])
    payload = {'schema_version': 1, 'observations_sha256': study.digest_file(arguments.observations),
               'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
               'scope': 'intersecting historical growth and technology touch events; no fill validation',
               'bollinger_ddof': 1, 'results': results}
    account.write_result(arguments.result, payload)
    print(json.dumps(payload, ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('database', 'calendar', 'qfq-dir', 'manifest', 'input', 'observations', 'result'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
