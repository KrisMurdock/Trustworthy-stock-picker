#!/usr/bin/env python3
"""Replay fixed exit experiments for the locked daily-bar limit-up candidate."""

import argparse
import collections
import json
import pathlib
import sqlite3
import time
from dataclasses import dataclass

import numpy as np

import limit_up_study as study


@dataclass(frozen=True)
class Policy:
    name: str
    stop: float
    target: float
    max_hold: int


POLICIES = (
    Policy('stop3_target6_max3', 0.03, 0.06, 3),
    Policy('stop4_target8_max3', 0.04, 0.08, 3),
    Policy('stop6_target12_max3', 0.06, 0.12, 3),
    Policy('stop5_target10_max5', 0.05, 0.10, 5),
)


def price_view(raw, adjusted):
    size = len(raw)
    cents = (raw[:, :4] + 5000) // 10000
    raw_open, raw_high, raw_low, raw_close = cents.T
    adj_open, adj_high, adj_low, adj_close, _ = adjusted.T
    raw_valid = (np.all(raw[:, :4] > 0, axis=1) & (raw[:, 4] > 0)
                 & np.all(raw[:, :4] % 10000 == 0, axis=1)
                 & (raw_high >= np.maximum(raw_open, raw_close))
                 & (raw_low <= np.minimum(raw_open, raw_close)) & (raw_high >= raw_low))
    adjusted_valid = (np.all(np.isfinite(adjusted), axis=1) & np.all(adjusted > 0, axis=1)
                      & (adj_high >= np.maximum(adj_open, adj_close))
                      & (adj_low <= np.minimum(adj_open, adj_close)) & (adj_high >= adj_low))
    with np.errstate(divide='ignore', invalid='ignore'):
        factor = adj_close / raw_close
        shape_error = np.maximum.reduce((np.abs(adj_open / (raw_open * factor) - 1),
                                         np.abs(adj_high / (raw_high * factor) - 1),
                                         np.abs(adj_low / (raw_low * factor) - 1)))
    valid = raw_valid & adjusted_valid & (shape_error <= 0.03)
    available = np.where(valid, np.arange(size), -1)
    previous = np.r_[-1, np.maximum.accumulate(available)[:-1]]
    has_previous = previous >= 0
    previous_safe = np.maximum(previous, 0)
    previous_close = np.where(has_previous, raw_close[previous_safe], 0)
    previous_adjusted = np.where(has_previous, adj_close[previous_safe], np.nan)
    with np.errstate(divide='ignore', invalid='ignore'):
        factor_error = np.abs((adj_open / raw_open) / (previous_adjusted / previous_close) - 1)
        tolerance = 0.002 + 0.0051 / adj_open + 0.0051 / previous_adjusted
    normal = valid & has_previous & (factor_error <= tolerance)
    down_five = study.limit_cents(previous_close, -5)
    return {'valid': valid, 'normal': normal, 'raw_open': raw_open, 'raw_low': raw_low,
            'previous_close': previous_close, 'down_five': down_five,
            'adj_open': adj_open, 'adj_high': adj_high, 'adj_low': adj_low}


def replay_policy(view, entry_position, entry_price, policy):
    stop = 1 - policy.stop
    target = 1 + policy.target
    expiry = entry_position + policy.max_hold
    pending = False
    blocked = False
    ambiguous = False
    for position in range(entry_position + 1, len(view['valid'])):
        if not view['valid'][position]:
            blocked = True
            continue
        open_ratio = view['adj_open'][position] / entry_price
        low_ratio = view['adj_low'][position] / entry_price
        high_ratio = view['adj_high'][position] / entry_price
        can_sell_open = view['normal'][position] and view['raw_open'][position] > view['down_five'][position]
        if not can_sell_open:
            blocked = True
            if position < expiry and low_ratio <= stop:
                pending = True
            continue
        if pending:
            return open_ratio, int(blocked), 'delayed_stop', ambiguous
        if position >= expiry:
            return open_ratio, int(blocked), 'expiry', ambiguous
        if open_ratio <= stop:
            return open_ratio, int(blocked), 'opening_stop', ambiguous
        if open_ratio >= target:
            return target, int(blocked), 'opening_target', ambiguous
        stop_hit = low_ratio <= stop
        target_hit = high_ratio >= target
        if stop_hit:
            ambiguous = ambiguous or target_hit
            if view['raw_low'][position] <= view['down_five'][position]:
                pending = True
                blocked = True
                continue
            return low_ratio, int(blocked), 'intraday_stop_at_low', ambiguous
        if target_hit:
            return target, int(blocked), 'intraday_target', ambiguous
    return np.nan, int(blocked), 'unresolved', ambiguous


def fixed_open_ratio(view, entry_position, entry_price, hold):
    for position in range(entry_position + hold, len(view['valid'])):
        if view['normal'][position] and view['raw_open'][position] > view['down_five'][position]:
            return view['adj_open'][position] / entry_price
    return np.nan


def run(arguments):
    output = arguments.output
    destination = output / 'exit-development.json'
    if destination.exists():
        raise ValueError('refusing to overwrite exit development result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    selection_path = output / 'selection.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    if metadata['limited'] or not metadata['database_sha256']:
        raise ValueError('exit replay requires a full verified extraction')
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
    config = selection['candidate']['config']
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    indices = np.flatnonzero(study.select_mask(events, config) & development)
    if not len(indices):
        raise ValueError('locked candidate has no development events')
    selected = {name: values[indices] for name, values in events.items()}
    selected['sale_ratio'] = np.full((len(indices), len(POLICIES)), np.nan)
    selected['delay'] = np.full((len(indices), len(POLICIES)), -1, dtype=np.int16)
    reasons = np.full((len(indices), len(POLICIES)), 'unresolved', dtype='<U24')
    ambiguous = np.zeros((len(indices), len(POLICIES)), dtype=bool)
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
            view = price_view(raw, adjusted)
            positions = np.flatnonzero(selected['code'] == code)
            for selected_index in positions:
                event_position = date_index[str(selected['date'][selected_index])]
                previous = view['previous_close'][event_position]
                upper = study.limit_cents(previous, int(config['board']))
                entry_price = upper * view['adj_open'][event_position] / view['raw_open'][event_position]
                for hold_index, hold in enumerate(study.HOLDS):
                    expected = events['sale_ratio'][indices[selected_index], hold_index]
                    actual = fixed_open_ratio(view, event_position, entry_price, hold)
                    if not np.isclose(expected, actual, rtol=0, atol=1e-10, equal_nan=True):
                        raise ValueError(f'fixed-exit replay differs: {code} {selected["date"][selected_index]}')
                for policy_index, policy in enumerate(POLICIES):
                    ratio, delay, reason, uncertain = replay_policy(view, event_position, entry_price, policy)
                    selected['sale_ratio'][selected_index, policy_index] = ratio
                    selected['delay'][selected_index, policy_index] = delay
                    reasons[selected_index, policy_index] = reason
                    ambiguous[selected_index, policy_index] = uncertain
                checked += 1
            if code_number % 100 == 0 or code_number == len(codes):
                print(f'已复核 {code_number}/{len(codes)} 只，{checked} 笔开发期事件', flush=True)
    finally:
        connection.close()
    years = np.asarray([int(date[:4]) for date in selected['date']], dtype=np.int16)
    periods = {'training_2021_2023': (years >= 2021) & (years <= 2023), 'selection_2024': years == 2024}
    records = []
    for policy_index, policy in enumerate(POLICIES):
        metrics = {}
        for name, period in periods.items():
            metrics[name] = {
                '10bp': study.summarize(selected, period, study.HOLDS[policy_index], 0.001),
                '20bp': study.summarize(selected, period, study.HOLDS[policy_index], 0.002),
                'reason_counts': dict(collections.Counter(reasons[period, policy_index].tolist())),
                'same_day_both_hit': int(np.count_nonzero(ambiguous[period, policy_index])),
                '20bp_day_cluster_ci': study.bootstrap_pf(selected, period, study.HOLDS[policy_index]),
            }
        training = metrics['training_2021_2023']['20bp']
        selection_stats = metrics['selection_2024']['20bp']
        sample_ok = (study.eligible(training, 300, 80, 50) and study.eligible(selection_stats, 80, 40, 25))
        score = study.candidate_score(training, selection_stats) if sample_ok else None
        records.append({'policy': policy.name, 'score': score, 'sample_ok': sample_ok,
                        'targets_passed': bool(sample_ok and score >= 1), 'periods': metrics})
    records.sort(key=lambda record: -1 if record['score'] is None else record['score'], reverse=True)
    result = {'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
              'selection_sha256': study.digest_file(selection_path), 'candidate': config,
              'development_events': checked, 'stocks': int(len(codes)), 'policies': records,
              'targets_passed_count': sum(record['targets_passed'] for record in records),
              'holdout_examined': False, 'elapsed_seconds': round(time.monotonic() - started, 2)}
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
