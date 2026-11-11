#!/usr/bin/env python3
"""Compare prespecified next-close exit rules on the locked limit-up candidate."""

import argparse
import json
import pathlib
import sqlite3
import time
from dataclasses import dataclass

import numpy as np

import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_minutes as minute_sample
import limit_up_study as study


@dataclass(frozen=True)
class Policy:
    name: str
    weak_below: float
    extend_at: float | None = None
    guard: str | None = None


POLICIES = (
    Policy('weak_minus_3_t2_else_t3', 0.97),
    Policy('weak_zero_t2_else_t3', 1.00),
    Policy('weak_plus_3_t2_else_t3', 1.03),
    Policy('weak_minus_3_t2_strong_8_t5_else_t3', 0.97, 1.08),
    Policy('weak_zero_t2_strong_8_t5_else_t3', 1.00, 1.08),
)

REBOUND_POLICIES = (
    Policy('weak_and_bearish_t2_else_t3', 1.00, guard='below_open'),
    Policy('weak_and_below_midpoint_t2_else_t3', 1.00, guard='below_midpoint'),
)


def development_indices(events):
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    return np.flatnonzero(study.select_mask(events, minute_sample.BASE_CONFIG) & development)


def next_close_ratio(view, raw, position, entry_cents):
    if position < 0 or position >= len(view['normal']) or not np.isfinite(entry_cents) or entry_cents <= 0:
        raise ValueError('invalid event entry')
    next_position = position + 1
    if next_position >= len(view['normal']) or not view['normal'][next_position]:
        return np.nan
    close_micros = raw[next_position, 3]
    if not np.isfinite(close_micros) or close_micros <= 0 or close_micros % 10000 != 0:
        return np.nan
    return float((close_micros // 10000) / entry_cents)


def next_candle_conditions(view, raw, position):
    next_position = position + 1
    if position < 0 or next_position >= len(view['normal']) or not view['normal'][next_position]:
        return False, False
    opening, high, low, close = (int(raw[next_position, column]) for column in range(4))
    return close < opening, 2 * close < high + low


def choose_holds(close_ratios, policy, guards=None):
    values = np.asarray(close_ratios, dtype=float)
    if values.ndim != 1:
        raise ValueError('next-close ratios must be one-dimensional')
    known = np.isfinite(values) & (values > 0)
    holds = np.full(len(values), 3, dtype=np.int8)
    weak = known & (values < policy.weak_below)
    if policy.guard is not None:
        if guards is None or policy.guard not in guards:
            raise ValueError('missing next-candle guard')
        guard = np.asarray(guards[policy.guard])
        if guard.shape != values.shape or guard.dtype != np.dtype(bool):
            raise ValueError('invalid next-candle guard')
        weak &= guard
    holds[weak] = 2
    if policy.extend_at is not None:
        holds[known & (values >= policy.extend_at)] = 5
    return holds


def apply_holds(events, holds):
    holds = np.asarray(holds)
    count = len(events['date'])
    if holds.shape != (count,) or np.any(~np.isin(holds, study.HOLDS)):
        raise ValueError('invalid exit hold choices')
    indices = np.searchsorted(study.HOLDS, holds)
    rows = np.arange(count)
    replay = dict(events)
    replay['sale_ratio'] = events['sale_ratio'].copy()
    replay['delay'] = events['delay'].copy()
    column = study.HOLDS.index(3)
    replay['sale_ratio'][:, column] = events['sale_ratio'][rows, indices]
    replay['delay'][:, column] = events['delay'][rows, indices]
    return replay


def compare_with_fixed(events, replay, holds, mask):
    column = study.HOLDS.index(3)
    baseline = study.net_returns(events['sale_ratio'][:, column], 0.002)
    alternative = study.net_returns(replay['sale_ratio'][:, column], 0.002)
    changed = mask & (holds != 3)
    comparable = changed & np.isfinite(baseline) & np.isfinite(alternative)
    delta = alternative[comparable] - baseline[comparable]
    return {
        'changed': int(np.count_nonzero(changed)),
        'comparable': int(np.count_nonzero(comparable)),
        'unresolved_comparisons': int(np.count_nonzero(changed & ~comparable)),
        'improved': int(np.count_nonzero(delta > 1e-12)),
        'worsened': int(np.count_nonzero(delta < -1e-12)),
        'unchanged': int(np.count_nonzero(np.abs(delta) <= 1e-12)),
        'loss_to_gain': int(np.count_nonzero(comparable & (baseline < 0) & (alternative > 0))),
        'gain_to_loss': int(np.count_nonzero(comparable & (baseline > 0) & (alternative < 0))),
        'mean_return_change': float(np.mean(delta)) if len(delta) else None,
    }


def summarize_policy(events, holds, close_ratios, candidate, periods):
    replay = apply_holds(events, holds)
    results = {}
    for name, period in periods.items():
        mask = candidate & period
        decision = holds[mask]
        results[name] = {
            '10bp': study.summarize(replay, mask, 3, 0.001),
            '20bp': study.summarize(replay, mask, 3, 0.002),
            '20bp_day_cluster_ci': study.bootstrap_pf(replay, mask, 3),
            'decision_counts': {
                't2': int(np.count_nonzero(decision == 2)),
                't3': int(np.count_nonzero(decision == 3)),
                't5': int(np.count_nonzero(decision == 5)),
                'missing_next_close': int(np.count_nonzero(~np.isfinite(close_ratios[mask]))),
            },
            'comparison_with_fixed_t3': compare_with_fixed(events, replay, holds, mask),
        }
    return results


def verify_baseline(events, candidate, periods, indicator):
    matches = [record for record in indicator['results']
               if record['filter'] == 'pdi_gt_mdi' and record['hold'] == 3]
    if len(matches) != 1:
        raise ValueError('locked indicator baseline is unavailable')
    for name in ('training_2021_2023', 'selection_2024'):
        expected = matches[0]['periods'][name]['20bp']
        actual = study.summarize(events, candidate & periods[name], 3, 0.002)
        for key in ('signals', 'closed', 'unresolved', 'days', 'stocks', 'wins', 'losses'):
            if actual[key] != expected[key]:
                raise ValueError('locked indicator sample differs from replay')
        for key in ('win_rate', 'mean_return', 'payoff', 'pf'):
            if actual[key] is None or expected[key] is None or not np.isclose(
                    actual[key], expected[key], rtol=0, atol=1e-12):
                raise ValueError('locked indicator results differ from replay')


def run(arguments):
    output = arguments.output
    destination = output / 'dynamic-exit-development.json'
    if destination.exists():
        raise ValueError('refusing to overwrite dynamic exit result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    selection_path = output / 'selection.json'
    indicator_path = output / 'indicator-development.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    indicator = json.loads(indicator_path.read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata['limited'] or not metadata['database_sha256']):
        raise ValueError('dynamic exit replay requires a full verified extraction')
    event_path = output / 'events.npz'
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (event_path, metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    if (selection['events_sha256'] != metadata['events_sha256']
            or indicator['events_sha256'] != metadata['events_sha256']
            or indicator['selection_sha256'] != study.digest_file(selection_path)
            or indicator['base'] != minute_sample.BASE_CONFIG):
        raise ValueError('locked indicator input mismatch')
    events = study.load_events(event_path)
    selected_indices = development_indices(events)
    if not len(selected_indices):
        raise ValueError('locked development events are unavailable')
    selected = {name: values[selected_indices] for name, values in events.items()}
    candidate = np.zeros(len(selected_indices), dtype=bool)
    close_ratios = np.full(len(selected_indices), np.nan)
    guards = {'below_open': np.zeros(len(selected_indices), dtype=bool),
              'below_midpoint': np.zeros(len(selected_indices), dtype=bool)}
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
                raise ValueError('missing frozen price manifest entry')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], dates[-1])
            view = exits.price_view(raw, adjusted)
            dmi = indicators.calculate(adjusted, view['valid'])['dmi_difference']
            for index in np.flatnonzero(selected['code'] == code):
                position = date_index[str(selected['date'][index])]
                if position <= 0:
                    raise ValueError('event has no previous trading session')
                checked += 1
                if not np.isfinite(dmi[position - 1]) or dmi[position - 1] <= 0:
                    continue
                upper = study.limit_cents(view['previous_close'][position], 20)
                entry = upper * view['adj_open'][position] / view['raw_open'][position]
                for hold_index, hold in enumerate(study.HOLDS):
                    expected = selected['sale_ratio'][index, hold_index]
                    actual = exits.fixed_open_ratio(view, position, entry, hold)
                    if not np.isclose(expected, actual, rtol=0, atol=1e-10, equal_nan=True):
                        raise ValueError('fixed exit replay differs from frozen events')
                candidate[index] = True
                close_ratios[index] = next_close_ratio(view, raw, position, upper)
                guards['below_open'][index], guards['below_midpoint'][index] = next_candle_conditions(view, raw, position)
            if code_number % 100 == 0 or code_number == len(codes):
                print(f'已核对 {code_number}/{len(codes)} 只、{checked} 笔开发期基础事件', flush=True)
    finally:
        connection.close()
    years = np.asarray([int(date[:4]) for date in selected['date']], dtype=np.int16)
    periods = {'training_2021_2023': (years >= 2021) & (years <= 2023),
               'selection_2024': years == 2024}
    periods.update({f'year_{year}': years == year for year in range(2021, 2025)})
    verify_baseline(selected, candidate, periods, indicator)
    records = []
    experiments = [('fixed_t3', np.full(len(selected_indices), 3, dtype=np.int8))]
    experiments.extend((policy.name, choose_holds(close_ratios, policy)) for policy in POLICIES)
    experiments.extend((policy.name, choose_holds(close_ratios, policy, guards)) for policy in REBOUND_POLICIES)
    for name, holds in experiments:
        metrics = summarize_policy(selected, holds, close_ratios, candidate, periods)
        training = metrics['training_2021_2023']['20bp']
        validation = metrics['selection_2024']['20bp']
        eligible = study.eligible(training, 300, 80, 50) and study.eligible(validation, 80, 40, 25)
        score = study.candidate_score(training, validation) if eligible else None
        passing = bool(eligible and score >= 1 and training['mean_return'] > 0 and validation['mean_return'] > 0)
        records.append({'policy': name, 'sample_ok': eligible, 'score': score,
                        'targets_passed': passing, 'periods': metrics})
    result = {'schema_version': 1, 'events_sha256': metadata['events_sha256'],
              'database_sha256': metadata['database_sha256'],
              'indicator_sha256': study.digest_file(indicator_path),
              'base': minute_sample.BASE_CONFIG, 'entry_filter': 'pdi_gt_mdi',
              'base_development_events': checked, 'candidate_development_events': int(candidate.sum()),
              'tested_dynamic_policies': len(POLICIES),
              'tested_rebound_policies': len(REBOUND_POLICIES),
              'targets_passed_count': sum(record['targets_passed'] for record in records[1:1 + len(POLICIES)]),
              'rebound_targets_passed_count': sum(record['targets_passed'] for record in records[1 + len(POLICIES):]),
              'holdout_signals_examined': False, 'results': records,
              'elapsed_seconds': round(time.monotonic() - started, 2)}
    with destination.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')
    print(json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False), flush=True)


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
