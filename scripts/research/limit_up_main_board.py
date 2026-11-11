#!/usr/bin/env python3
"""Compare a fixed set of mainland-board limit-up development hypotheses."""

import argparse
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_dynamic_exits as dynamic
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_st_analysis as analysis
import limit_up_study as study
import limit_up_volume as volume


FILTER_NAMES = (
    'none', 'pdi_gt_mdi', 'prior_volume_gt_ma5', 'prior_volume_le_ma5', 'prior_close_up',
    'pdi_and_volume_gt_ma5', 'pdi_and_volume_le_ma5', 'pdi_and_close_up', 'pdi_and_close_above_ma20',
)


def development_indices(events):
    return np.flatnonzero((events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
                          & (events['board'] == 10) & events['ma20_above_ma60']
                          & (events['gap'] >= 0) & (events['gap'] < 0.07))


def filter_masks(events, features):
    count = len(events['date'])
    all_known = np.ones(count, dtype=bool)
    dmi_known = np.isfinite(features['dmi_difference'])
    volume_known = np.isfinite(features['volume_ratio'])
    close_known = features['close_known']
    pdi = dmi_known & (features['dmi_difference'] > 0)
    high_volume = volume_known & (features['volume_ratio'] > 1)
    low_volume = volume_known & (features['volume_ratio'] <= 1)
    close_up = close_known & features['close_up']
    result = {
        'none': (all_known, all_known),
        'pdi_gt_mdi': (pdi, dmi_known),
        'prior_volume_gt_ma5': (high_volume, volume_known),
        'prior_volume_le_ma5': (low_volume, volume_known),
        'prior_close_up': (close_up, close_known),
        'pdi_and_volume_gt_ma5': (pdi & high_volume, dmi_known & volume_known),
        'pdi_and_volume_le_ma5': (pdi & low_volume, dmi_known & volume_known),
        'pdi_and_close_up': (pdi & close_up, dmi_known & close_known),
        'pdi_and_close_above_ma20': (pdi & events['above_ma20'], dmi_known),
    }
    if tuple(result) != FILTER_NAMES:
        raise ValueError('filters differ from the locked contract')
    return result


def annual_and_uncertainty_gate(periods):
    for year in range(2021, 2025):
        stats = periods[f'year_{year}']['20bp']
        if (stats['closed'] < 30 or stats['days'] < 20 or stats['stocks'] < 20
                or stats['unresolved'] != 0 or stats['pf'] is None or stats['pf'] <= 1):
            return False
    for name in ('training_2021_2023', 'selection_2024'):
        interval = periods[name]['20bp_day_cluster_ci']
        if interval is None or interval['pf_95'][0] <= 1:
            return False
    return True


def evaluate(events, features, dynamic_holds):
    periods = analysis.period_masks(events['date'])
    masks = filter_masks(events, features)
    replays = {'fixed_t3': events, 'weak_zero_t2_else_t3': dynamic.apply_holds(events, dynamic_holds)}
    openings = {'zero_to_three': (events['gap'] >= 0) & (events['gap'] < 0.03),
                'three_to_seven': (events['gap'] >= 0.03) & (events['gap'] < 0.07)}
    results = []
    for opening_name, opening in openings.items():
        for filter_name in FILTER_NAMES:
            accepted, known = masks[filter_name]
            for exit_name, replay in replays.items():
                metrics = analysis.summarize_periods(replay, opening & accepted, periods)
                training = metrics['training_2021_2023']['20bp']
                selection = metrics['selection_2024']['20bp']
                sample_ok = study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)
                score = study.candidate_score(training, selection) if sample_ok else None
                targets = bool(sample_ok and score >= 1 and training['mean_return'] > 0
                               and selection['mean_return'] > 0)
                annual_ok = annual_and_uncertainty_gate(metrics)
                results.append({
                    'opening_gap': opening_name, 'filter': filter_name, 'exit': exit_name,
                    'missing_filter_events': int(np.count_nonzero(opening & ~known)),
                    'sample_ok': sample_ok, 'score': score, 'targets_passed': targets,
                    'annual_and_uncertainty_passed': annual_ok,
                    'strict_development_passed': targets and annual_ok,
                    'periods': metrics,
                })
    return results


def run(arguments):
    started = time.monotonic()
    output = arguments.output
    if arguments.result.exists():
        raise ValueError('refusing to overwrite main-board result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata.get('limited') or not metadata.get('database_sha256')):
        raise ValueError('analysis requires a full verified extraction')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (output / 'events.npz', metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    events = study.load_events(output / 'events.npz')
    selected_indices = development_indices(events)
    selected = {name: values[selected_indices] for name, values in events.items()}
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    date_index = {date: index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    count = len(selected_indices)
    if not count:
        raise ValueError('development events are unavailable')
    features = {name: np.full(count, np.nan) for name in ('dmi_difference', 'volume_ratio')}
    features.update({name: np.zeros(count, dtype=bool) for name in ('close_known', 'close_up')})
    close_ratios = np.full(count, np.nan)
    checked = 0
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        codes = np.unique(selected['code'])
        for number, code in enumerate(codes, start=1):
            code = str(code)
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest:
                raise ValueError('missing frozen adjusted-price manifest entry')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], dates[-1])
            view = exits.price_view(raw, adjusted)
            previous_indicators = indicators.calculate(adjusted, view['valid'])
            previous_volume = volume.calculate(adjusted, view['valid'])
            for index in np.flatnonzero(selected['code'] == code):
                position = date_index[str(selected['date'][index])]
                prior = analysis.features_before_signal(position, previous_indicators, previous_volume)
                for name in features:
                    features[name][index] = prior[name]
                upper = study.limit_cents(view['previous_close'][position], 10)
                entry = upper * view['adj_open'][position] / view['raw_open'][position]
                for column, hold in enumerate(study.HOLDS):
                    actual = exits.fixed_open_ratio(view, position, entry, hold)
                    expected = selected['sale_ratio'][index, column]
                    if not np.isclose(actual, expected, rtol=0, atol=1e-10, equal_nan=True):
                        raise ValueError('fixed exit differs from frozen event')
                close_ratios[index] = dynamic.next_close_ratio(view, raw, position, upper)
                checked += 1
            if number % 500 == 0 or number == len(codes):
                print(f'已核对 {number}/{len(codes)} 只主板股票、{checked} 笔开发期事件', flush=True)
    finally:
        connection.close()
    if checked != count:
        raise ValueError('main-board event replay is incomplete')
    holds = dynamic.choose_holds(close_ratios, dynamic.POLICIES[1])
    records = evaluate(selected, features, holds)
    result = {
        'schema_version': 1, 'events_sha256': metadata['events_sha256'],
        'database_sha256': metadata['database_sha256'], 'development_events': count,
        'development_stocks': len(codes), 'historical_st_status': 'unknown',
        'tested_combinations': len(records), 'sample_eligible_combinations': sum(record['sample_ok'] for record in records),
        'targets_passed_count': sum(record['targets_passed'] for record in records),
        'strict_development_passed_count': sum(record['strict_development_passed'] for record in records),
        'holdout_signals_examined': False, 'results': records,
        'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    with arguments.result.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')
    print(json.dumps({key: result[key] for key in ('development_events', 'development_stocks',
                     'tested_combinations', 'sample_eligible_combinations', 'targets_passed_count',
                     'strict_development_passed_count')}, ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=pathlib.Path, required=True)
    parser.add_argument('--calendar', type=pathlib.Path, required=True)
    parser.add_argument('--qfq-dir', type=pathlib.Path, required=True)
    parser.add_argument('--manifest', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
