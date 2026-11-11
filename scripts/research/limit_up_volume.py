#!/usr/bin/env python3
"""Compare prespecified prior-day volume filters for limit-up events."""

import argparse
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_exits as exits
import limit_up_study as study


def calculate(adjusted, valid):
    valid = np.asarray(valid, dtype=bool)
    if adjusted.shape != (len(valid), 5):
        raise ValueError('invalid adjusted prices or validity shape')
    average = study.rolling_mean(adjusted[:, 4], valid, 5)
    ratio = np.divide(adjusted[:, 4], average, out=np.full(len(valid), np.nan),
                      where=valid & np.isfinite(average) & (average > 0))
    prior_valid = np.r_[False, valid[:-1]]
    prior_close = np.r_[np.nan, adjusted[:-1, 3]]
    close_known = valid & prior_valid
    return {'volume_ratio': ratio, 'close_known': close_known,
            'close_up': close_known & (adjusted[:, 3] > prior_close)}


def filter_masks(events, features):
    ratio = features['volume_ratio']
    known = np.isfinite(ratio)
    higher = known & (ratio > 1)
    lower = known & (ratio <= 1)
    close_up = features['close_known'] & features['close_up']
    trend = events['ma20_above_ma60']
    return {
        'none': np.ones(len(ratio), dtype=bool),
        'volume_gt_ma5': higher,
        'volume_le_ma5': lower,
        'prior_close_up': close_up,
        'volume_gt_ma5_and_close_up': higher & close_up,
        'volume_le_ma5_and_close_up': lower & close_up,
        'volume_gt_ma5_and_ma20_gt_ma60': higher & trend,
        'volume_le_ma5_and_ma20_gt_ma60': lower & trend,
    }


def run(arguments):
    output = arguments.output
    destination = output / 'volume-development.json'
    if destination.exists():
        raise ValueError('refusing to overwrite volume development result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata['limited'] or not metadata['database_sha256']):
        raise ValueError('volume replay requires a full verified extraction')
    if study.digest_file(arguments.database) != metadata['database_sha256']:
        raise ValueError('frozen database changed after extraction')
    event_path = output / 'events.npz'
    if study.digest_file(event_path) != metadata['events_sha256']:
        raise ValueError('event data changed after extraction')
    if study.digest_file(arguments.calendar) != metadata['calendar_sha256']:
        raise ValueError('calendar changed after extraction')
    if study.digest_file(arguments.manifest) != metadata['manifest_sha256']:
        raise ValueError('price manifest changed after extraction')
    events = study.load_events(event_path)
    base = {'board': 20, 'prior': 'any', 'trend': 'any', 'rsi': 'any',
            'gap': 'three_to_seven', 'hold': 1}
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    indices = np.flatnonzero(study.select_mask(events, base) & development)
    if not len(indices):
        raise ValueError('no development events for volume experiment')
    selected = {name: values[indices] for name, values in events.items()}
    features = {'volume_ratio': np.full(len(indices), np.nan),
                'close_known': np.zeros(len(indices), dtype=bool),
                'close_up': np.zeros(len(indices), dtype=bool)}
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
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
            history = calculate(adjusted, exits.price_view(raw, adjusted)['valid'])
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
    for name, filter_mask in filter_masks(selected, features).items():
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
            sample_ok = (study.eligible(training, 300, 80, 50)
                         and study.eligible(selection_stats, 80, 40, 25))
            score = study.candidate_score(training, selection_stats) if sample_ok else None
            records.append({'filter': name, 'hold': hold, 'sample_ok': sample_ok, 'score': score,
                            'targets_passed': bool(sample_ok and score >= 1), 'periods': metrics})
    records.sort(key=lambda record: -1 if record['score'] is None else record['score'], reverse=True)
    result = {'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
              'base': base, 'development_events': checked, 'stocks': int(len(codes)), 'tested': len(records),
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
