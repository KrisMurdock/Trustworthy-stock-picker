#!/usr/bin/env python3
"""Evaluate preregistered limit-up indicator hypotheses on frozen events."""

import argparse
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_account_sharpe as account
import limit_up_board_analysis as board
import limit_up_dynamic_exits as dynamic
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_study as study
import limit_up_win_analysis as win


FIRST_DATE = '2021-01-01'
END_DATE = '2025-01-01'
BRANCHES = ('first_trend', 'first_rebound', 'one_to_two', 'two_or_more')
ENHANCEMENTS = ('none', 'dmi', 'atr', 'dmi_atr')
EXIT_NAMES = ('t1', 'hold3', 'hold5', 'weak4_t2_else_t3')
PERIODS = {'training_2021_2023': ('2021-01-01', '2024-01-01'),
           'year_2021': ('2021-01-01', '2022-01-01'),
           'year_2022': ('2022-01-01', '2023-01-01'),
           'year_2023': ('2023-01-01', '2024-01-01'),
           'selection_2024': ('2024-01-01', '2025-01-01')}


def bollinger(close, valid, width=20, ddof=0):
    close = np.asarray(close, dtype=float)
    valid = np.asarray(valid, dtype=bool)
    if (close.ndim != 1 or valid.shape != close.shape or width < 2
            or type(ddof) is not int or not 0 <= ddof < width):
        raise ValueError('invalid Bollinger input')
    middle = np.full(len(close), np.nan)
    upper = np.full(len(close), np.nan)
    lower = np.full(len(close), np.nan)
    if len(close) >= width:
        windows = np.lib.stride_tricks.sliding_window_view(close, width)
        validity = np.lib.stride_tricks.sliding_window_view(valid & np.isfinite(close), width)
        complete = np.all(validity, axis=1)
        means = np.mean(windows[complete], axis=1)
        deviations = np.std(windows[complete], axis=1, ddof=ddof)
        positions = np.flatnonzero(complete) + width - 1
        middle[positions] = means
        upper[positions] = means + 2 * deviations
        lower[positions] = means - 2 * deviations
    return middle, upper, lower


def branch_masks(features, alternate=False):
    previous_known = np.asarray(features['previous_known'], dtype=bool)
    previous_sealed = np.asarray(features['previous_sealed'], dtype=bool)
    earlier_known = np.asarray(features['earlier_known'], dtype=bool)
    earlier_sealed = np.asarray(features['earlier_sealed'], dtype=bool)
    if (np.any(previous_sealed & ~previous_known)
            or np.any(earlier_sealed & ~earlier_known)):
        raise ValueError('unknown board state cannot be sealed')
    macd = features['macd']
    earlier_macd = features['earlier_macd']
    rsi = features['rsi']
    earlier_rsi = features['earlier_rsi']
    close = features['close']
    middle = features['middle']
    upper = features['upper']
    lower = features['lower']
    first = previous_known & ~previous_sealed
    continuation = previous_known & previous_sealed & earlier_known
    trend_low, trend_high = (55, 80) if alternate else (50, 75)
    rebound_low, rebound_high = (35, 60) if alternate else (30, 55)
    follow_low, follow_high = (60, 90) if alternate else (55, 85)
    trend = ((macd > 0) & (macd > earlier_macd) & (rsi >= trend_low) & (rsi < trend_high)
             & (close >= middle) & (close < upper))
    rebound = ((macd < 0) & (macd > earlier_macd) & (rsi >= rebound_low) & (rsi < rebound_high)
               & (rsi > earlier_rsi) & (close >= lower) & (close < middle))
    follow = ((macd > 0) & (rsi >= follow_low) & (rsi < follow_high) & (close >= middle))
    return {
        'first_trend': first & trend,
        'first_rebound': first & rebound,
        'one_to_two': continuation & ~earlier_sealed & follow,
        'two_or_more': continuation & earlier_sealed & follow,
    }


def enhancement_masks(features):
    dmi = np.isfinite(features['dmi']) & (features['dmi'] > 0)
    atr = np.isfinite(features['atr']) & (features['atr'] >= 0.02) & (features['atr'] < 0.06)
    return {'none': np.ones(len(dmi), dtype=bool), 'dmi': dmi, 'atr': atr, 'dmi_atr': dmi & atr}


def weak_exit(events, close_ratio):
    close_ratio = np.asarray(close_ratio, dtype=float)
    count = len(events['date'])
    if close_ratio.shape != (count,):
        raise ValueError('invalid next-close ratios')
    known = np.isfinite(close_ratio) & (close_ratio > 0)
    selected = np.where(close_ratio <= 0.96, study.HOLDS.index(2), study.HOLDS.index(3))
    indexes = np.arange(count)
    ratios = np.where(known, events['sale_ratio'][indexes, selected], np.nan)
    delays = np.where(known, events['delay'][indexes, selected], -1).astype(np.int16)
    return ratios, delays


def status_masks(events, statuses):
    known_non_st = np.zeros(len(events['date']), dtype=bool)
    known_st = np.zeros(len(events['date']), dtype=bool)
    for index, (code, date) in enumerate(zip(events['code'], events['date'])):
        value = statuses.get((str(code), str(date)))
        if value is not None and type(value) is not bool:
            raise ValueError('invalid historical ST value')
        known_non_st[index] = value is False
        known_st[index] = value is True
    return known_non_st, known_st, ~(known_non_st | known_st)


def check_inputs(arguments):
    metadata = json.loads((arguments.input / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata.get('limited') or not metadata.get('database_sha256')):
        raise ValueError('full frozen event extraction is required')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (arguments.input / 'events.npz', metadata['events_sha256']))
    for path, expected in checks:
        if study.digest_file(path) != expected:
            raise ValueError('frozen input hash mismatch')
    records, statuses, _, evidence = win.load_evidence(arguments, metadata)
    return metadata, records, statuses, evidence


def extract_features(arguments, events, dates, manifest):
    count = len(events['date'])
    names = ('macd', 'earlier_macd', 'rsi', 'earlier_rsi', 'close', 'middle', 'upper', 'lower',
             'dmi', 'atr', 'next_close_ratio', 'hold3_ratio', 'hold5_ratio')
    features = {name: np.full(count, np.nan) for name in names}
    for name in ('previous_known', 'previous_sealed', 'earlier_known', 'earlier_sealed'):
        features[name] = np.zeros(count, dtype=bool)
    features['hold3_delay'] = np.full(count, -1, dtype=np.int16)
    features['hold5_delay'] = np.full(count, -1, dtype=np.int16)
    date_index = {str(date): index for index, date in enumerate(dates)}
    unique_codes, inverse = np.unique(events['code'], return_inverse=True)
    order = np.argsort(inverse, kind='stable')
    ends = np.r_[0, np.cumsum(np.bincount(inverse, minlength=len(unique_codes)))]
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        for number, code_value in enumerate(unique_codes):
            code = str(code_value)
            indices = order[ends[number]:ends[number + 1]]
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest or not path.exists():
                raise ValueError('frozen adjusted history is missing')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], str(dates[-1]))
            view = exits.price_view(raw, adjusted)
            calculated = indicators.calculate(adjusted, view['valid'])
            relative = study.rsi6(adjusted[:, 3], view['valid'])
            middle, upper, lower = bollinger(adjusted[:, 3], view['valid'])
            percent = study.limit_board(code)
            known, sealed = board.board_states(raw, adjusted, percent, view)
            positions = np.asarray([date_index[str(events['date'][index])] for index in indices])
            if np.any(positions < 2):
                raise ValueError('event lacks prior trading sessions')
            previous = positions - 1
            earlier = positions - 2
            if not np.allclose(relative[previous], events['rsi6'][indices], rtol=0, atol=1e-9, equal_nan=True):
                raise ValueError('RSI differs from frozen extraction')
            features['macd'][indices] = calculated['macd'][previous]
            features['earlier_macd'][indices] = calculated['macd'][earlier]
            features['rsi'][indices] = relative[previous]
            features['earlier_rsi'][indices] = relative[earlier]
            features['close'][indices] = adjusted[previous, 3]
            features['middle'][indices] = middle[previous]
            features['upper'][indices] = upper[previous]
            features['lower'][indices] = lower[previous]
            features['dmi'][indices] = calculated['dmi_difference'][previous]
            features['atr'][indices] = calculated['atr_ratio'][previous]
            features['previous_known'][indices] = known[previous]
            features['previous_sealed'][indices] = sealed[previous]
            features['earlier_known'][indices] = known[earlier]
            features['earlier_sealed'][indices] = sealed[earlier]
            for position, index in zip(positions, indices):
                if not known[position] or bool(sealed[position]) != bool(events['closed_at_limit'][index]):
                    raise ValueError('entry board differs from frozen event')
                if known[position - 1] and (
                        not events['prior_reference_valid'][index]
                        or bool(sealed[position - 1]) != bool(events['prior_limit'][index])):
                    raise ValueError('previous board differs from frozen event')
                entry_cents = study.limit_cents(view['previous_close'][position], percent)
                features['next_close_ratio'][index] = dynamic.next_close_ratio(view, raw, position, entry_cents)
                for horizon in (3, 5):
                    ratio, delay, _, _ = board.hold_until_break_exit(
                        view, known, sealed, position, percent, max_hold=horizon)
                    features[f'hold{horizon}_ratio'][index] = ratio
                    features[f'hold{horizon}_delay'][index] = delay
            if (number + 1) % 250 == 0 or number + 1 == len(unique_codes):
                print(f'Scanned {number + 1}/{len(unique_codes)} stocks', flush=True)
    finally:
        connection.close()
    return features


def exit_events(events, features, name):
    if name == 't1':
        ratios, delays = events['sale_ratio'][:, study.HOLDS.index(1)], events['delay'][:, study.HOLDS.index(1)]
    elif name in ('hold3', 'hold5'):
        ratios, delays = features[name + '_ratio'], features[name + '_delay']
    elif name == 'weak4_t2_else_t3':
        ratios, delays = weak_exit(events, features['next_close_ratio'])
    else:
        raise ValueError('unknown exit policy')
    result = dict(events)
    result['sale_ratio'] = events['sale_ratio'].copy()
    result['delay'] = events['delay'].copy()
    result['sale_ratio'][:, 0] = ratios
    result['delay'][:, 0] = delays
    return result


def evaluate(events, features, statuses):
    known_non_st, known_st, unknown = status_masks(events, statuses)
    branches = {alternate: branch_masks(features, alternate) for alternate in (False, True)}
    enhancement = enhancement_masks(features)
    replays = {name: exit_events(events, features, name) for name in EXIT_NAMES}
    markets = {
        'all': np.ones(len(events['date']), dtype=bool),
        'main': events['board'] == 10,
        'growth': np.char.startswith(events['code'], 'sz30'),
        'technology': np.char.startswith(events['code'], 'sh688'),
    }
    results = []
    for branch in BRANCHES:
        for alternate in (False, True):
            for extra in ENHANCEMENTS:
                selected = branches[alternate][branch] & enhancement[extra]
                for exit_name in EXIT_NAMES:
                    groups = {}
                    for period, (start, end) in PERIODS.items():
                        dated = (events['date'] >= start) & (events['date'] < end)
                        for market, market_mask in markets.items():
                            mask = selected & dated & market_mask
                            label = period + '/' + market
                            groups[label] = {
                                'status_known_non_st': int(np.count_nonzero(mask & known_non_st)),
                                'status_known_st': int(np.count_nonzero(mask & known_st)),
                                'status_unknown': int(np.count_nonzero(mask & unknown)),
                                'all_statuses_diagnostic': study.summarize(replays[exit_name], mask, 1),
                                'known_non_st_selected_subset': study.summarize(
                                    replays[exit_name], mask & known_non_st, 1),
                            }
                    results.append({'branch': branch, 'alternate_rsi': alternate, 'enhancement': extra,
                                    'exit': exit_name, 'groups': groups})
    return results


def run(arguments):
    started = time.monotonic()
    if arguments.result.exists():
        raise ValueError('refusing to overwrite result')
    metadata, records, statuses, evidence = check_inputs(arguments)
    all_events = study.load_events(arguments.input / 'events.npz')
    development = (all_events['date'] >= FIRST_DATE) & (all_events['date'] < END_DATE)
    events = {name: values[development] for name, values in all_events.items()}
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if len(dates) < 100 or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('invalid frozen trading calendar')
    manifest = study.load_manifest(arguments.manifest)
    features = extract_features(arguments, events, dates, manifest)
    result = {
        'schema_version': 1,
        'events_sha256': metadata['events_sha256'],
        'database_sha256': metadata['database_sha256'],
        'calendar_sha256': metadata['calendar_sha256'],
        'manifest_sha256': metadata['manifest_sha256'],
        'evidence': evidence,
        'status_records': len(statuses),
        'previous_candidate_records': len(records),
        'signal_first': FIRST_DATE,
        'signal_before': END_DATE,
        'holdout_signals_examined': False,
        'account_metrics_available': False,
        'account_metrics_reason': 'historical ST for premarket candidates and executable queue fills are unavailable',
        'warning': ('all-status event metrics are diagnostic only; known non-ST subset was selected by an '
                    'earlier strategy; ATR and alternate parameter compatibility are not verified'),
        'cost': '20bp per side friction proxy plus 20bp round-trip fee',
        'exit_limit_proxy': '5 percent for all boards; not a verified historical limit or fill',
        'bollinger': '20 sessions, population standard deviation, 2 standard deviations',
        'development_events': len(events['date']),
        'result_count': len(BRANCHES) * 2 * len(ENHANCEMENTS) * len(EXIT_NAMES),
        'results': evaluate(events, features, statuses),
        'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    if len(result['results']) != result['result_count']:
        raise ValueError('unexpected research configuration count')
    account.write_result(arguments.result, result)
    print(json.dumps({key: value for key, value in result.items() if key != 'results'},
                     ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('database', 'calendar', 'qfq-dir', 'manifest', 'input', 'candidates', 'sample', 'reference', 'result'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    parser.add_argument('--statuses', type=pathlib.Path, nargs='+', required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
