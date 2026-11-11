#!/usr/bin/env python3
"""Compare prespecified prior-day indicators for a frozen limit-up entry."""

import argparse
import json
import pathlib
import sqlite3
import time

import numpy as np
from scipy.signal import lfilter

import limit_up_exits as exits
import limit_up_study as study


def smooth(values, seed, weight):
    values = np.asarray(values, dtype=float)
    if not len(values):
        return np.asarray([], dtype=float)
    return lfilter([weight], [1.0, -(1.0 - weight)], values,
                   zi=[(1.0 - weight) * seed])[0]


def ema(values, width):
    values = np.asarray(values, dtype=float)
    result = np.full(len(values), np.nan)
    if len(values) >= width:
        seed = float(np.mean(values[:width]))
        result[width - 1] = seed
        result[width:] = smooth(values[width:], seed, 2 / (width + 1))
    return result


def calculate(adjusted, valid):
    size = len(adjusted)
    result = {name: np.full(size, np.nan) for name in ('macd', 'kdj_k', 'dmi_difference', 'atr_ratio')}
    result['close_up'] = np.zeros(size, dtype=bool)
    changes = np.diff(np.r_[False, valid, False].astype(np.int8))
    starts = np.flatnonzero(changes == 1)
    ends = np.flatnonzero(changes == -1)
    for start, end in zip(starts, ends):
        if end - start < 2:
            continue
        high = adjusted[start:end, 1]
        low = adjusted[start:end, 2]
        close = adjusted[start:end, 3]
        result['close_up'][start + 1:end] = close[1:] > close[:-1]
        if len(close) >= 34:
            difference = ema(close, 12) - ema(close, 26)
            result['macd'][start + 25:end] = 2 * (difference[25:] - ema(difference[25:], 9))
        if len(close) >= 9:
            highest = np.max(np.lib.stride_tricks.sliding_window_view(high, 9), axis=1)
            lowest = np.min(np.lib.stride_tricks.sliding_window_view(low, 9), axis=1)
            spread = highest - lowest
            stochastic = np.divide((close[8:] - lowest) * 100, spread,
                                   out=np.full(len(spread), 50.0), where=spread > 0)
            result['kdj_k'][start + 8:end] = smooth(stochastic, 50.0, 1 / 3)
        true_range = np.r_[high[0] - low[0], np.maximum.reduce((high[1:] - low[1:],
                             np.abs(high[1:] - close[:-1]), np.abs(low[1:] - close[:-1])))]
        upward = np.r_[0.0, np.diff(high)]
        downward = np.r_[0.0, -np.diff(low)]
        positive = np.where((upward > 0) & (upward > downward), upward, 0.0)
        negative = np.where((downward > 0) & (downward > upward), downward, 0.0)
        all_valid = np.ones(len(close), dtype=bool)
        atr = study.rolling_mean(true_range, all_valid, 14)
        plus = study.rolling_mean(positive, all_valid, 14)
        minus = study.rolling_mean(negative, all_valid, 14)
        result['atr_ratio'][start:end] = atr / close
        result['dmi_difference'][start:end] = np.divide(plus - minus, atr,
                                                        out=np.full(len(close), np.nan), where=atr > 0)
    return result


def filter_masks(features, rsi):
    macd = features['macd'] > 0
    kdj50 = features['kdj_k'] >= 50
    kdj70 = features['kdj_k'] >= 70
    rsi50 = rsi >= 50
    dmi = features['dmi_difference'] > 0
    atr = (features['atr_ratio'] >= 0.02) & (features['atr_ratio'] < 0.06)
    return {
        'none': np.ones(len(rsi), dtype=bool),
        'rsi_ge_50': rsi50,
        'macd_red': macd,
        'kdj_k_ge_50': kdj50,
        'kdj_k_ge_70': kdj70,
        'pdi_gt_mdi': dmi,
        'atr_2_to_6_pct': atr,
        'prior_close_up': features['close_up'],
        'macd_red_and_k_ge_70': macd & kdj70,
        'k_ge_70_and_rsi_ge_50': kdj70 & rsi50,
        'pdi_gt_mdi_and_atr_2_to_6_pct': dmi & atr,
    }


def run(arguments):
    output = arguments.output
    destination = output / 'indicator-development.json'
    if destination.exists():
        raise ValueError('refusing to overwrite indicator development result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    selection_path = output / 'selection.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    if metadata['limited'] or not metadata['database_sha256']:
        raise ValueError('indicator replay requires a full verified extraction')
    if study.digest_file(arguments.database) != metadata['database_sha256']:
        raise ValueError('frozen database changed after extraction')
    event_path = output / 'events.npz'
    if study.digest_file(event_path) != metadata['events_sha256'] or selection['events_sha256'] != metadata['events_sha256']:
        raise ValueError('event data changed after selection')
    if study.digest_file(arguments.calendar) != metadata['calendar_sha256']:
        raise ValueError('calendar changed after extraction')
    if study.digest_file(arguments.manifest) != metadata['manifest_sha256']:
        raise ValueError('price manifest changed after extraction')
    events = study.load_events(event_path)
    candidate = selection['candidate']['config']
    expected = {'board': 20, 'prior': 'any', 'trend': 'ma20_above_ma60',
                'rsi': 'at_least_50', 'gap': 'three_to_seven', 'hold': 3}
    if candidate != expected:
        raise ValueError('locked candidate differs from the indicator experiment contract')
    base = dict(candidate, rsi='any')
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    indices = np.flatnonzero(study.select_mask(events, base) & development)
    if not len(indices):
        raise ValueError('no development events for indicator experiment')
    selected = {name: values[indices] for name, values in events.items()}
    feature_names = ('macd', 'kdj_k', 'dmi_difference', 'atr_ratio')
    features = {name: np.full(len(indices), np.nan) for name in feature_names}
    features['close_up'] = np.zeros(len(indices), dtype=bool)
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    date_index = {date: index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    codes = np.unique(selected['code'])
    checked = 0
    started = time.monotonic()
    try:
        for code_number, code in enumerate(codes, start=1):
            code = str(code)
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest:
                raise ValueError(f'missing frozen price manifest entry: {code}')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], dates[-1])
            valid = exits.price_view(raw, adjusted)['valid']
            history = calculate(adjusted, valid)
            for selected_index in np.flatnonzero(selected['code'] == code):
                previous = date_index[str(selected['date'][selected_index])] - 1
                if previous < 0:
                    raise ValueError('event has no previous trading session')
                for name in features:
                    features[name][selected_index] = history[name][previous]
                checked += 1
            if code_number % 100 == 0 or code_number == len(codes):
                print(f'已计算 {code_number}/{len(codes)} 只，{checked} 笔开发期事件', flush=True)
    finally:
        connection.close()
    years = np.asarray([int(date[:4]) for date in selected['date']], dtype=np.int16)
    periods = {'training_2021_2023': (years >= 2021) & (years <= 2023), 'selection_2024': years == 2024}
    records = []
    for name, filter_mask in filter_masks(features, selected['rsi6']).items():
        for hold in (1, 3):
            metrics = {}
            for period_name, period in periods.items():
                metrics[period_name] = {
                    '10bp': study.summarize(selected, filter_mask & period, hold, 0.001),
                    '20bp': study.summarize(selected, filter_mask & period, hold, 0.002),
                    '20bp_day_cluster_ci': study.bootstrap_pf(selected, filter_mask & period, hold),
                }
            training = metrics['training_2021_2023']['20bp']
            selection_stats = metrics['selection_2024']['20bp']
            sample_ok = (study.eligible(training, 300, 80, 50) and study.eligible(selection_stats, 80, 40, 25))
            score = study.candidate_score(training, selection_stats) if sample_ok else None
            records.append({'filter': name, 'hold': hold, 'sample_ok': sample_ok, 'score': score,
                            'targets_passed': bool(sample_ok and score >= 1), 'periods': metrics})
    records.sort(key=lambda record: -1 if record['score'] is None else record['score'], reverse=True)
    result = {'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
              'selection_sha256': study.digest_file(selection_path), 'base': base,
              'development_events': checked, 'stocks': int(len(codes)), 'tested': len(records),
              'eligible': sum(record['sample_ok'] for record in records),
              'targets_passed_count': sum(record['targets_passed'] for record in records),
              'results': records, 'holdout_examined': False, 'elapsed_seconds': round(time.monotonic() - started, 2)}
    destination.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=pathlib.Path, required=True)
    parser.add_argument('--calendar', type=pathlib.Path, required=True)
    parser.add_argument('--qfq-dir', type=pathlib.Path, required=True)
    parser.add_argument('--manifest', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
