#!/usr/bin/env python3
"""Audit completed five-minute paths without assuming order fills."""

import argparse
import collections
import datetime
import json
import math
import pathlib
import sqlite3

import numpy as np

import limit_up_minutes as minute_sample
import limit_up_study as study


def cents(value):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value <= 0:
        raise ValueError('invalid minute price')
    scaled = value * 100
    result = round(scaled)
    if abs(scaled - result) > 0.001:
        raise ValueError('minute price is not in cents')
    return result


def session_ends(day):
    date = datetime.date.fromisoformat(day)
    morning = datetime.datetime.combine(date, datetime.time(9, 30))
    afternoon = datetime.datetime.combine(date, datetime.time(13, 0))
    return [start + datetime.timedelta(minutes=5 * interval)
            for start in (morning, afternoon) for interval in range(1, 25)]


def classify_path(record, response):
    if response is None:
        return {'quality': 'missing_result'}
    if response.get('status') != 'ok':
        return {'quality': 'source_unavailable'}
    archive = response.get('archive') or {}
    expected = {'code': record['code'], 'source': 'baostock', 'period_minutes': 5,
                'base_period_minutes': 5, 'adjust': 'none', 'from': record['date'], 'through': record['date']}
    if any(archive.get(key) != value for key, value in expected.items()):
        return {'quality': 'archive_metadata_mismatch'}
    bars = archive.get('bars')
    if not isinstance(bars, list) or len(bars) != 48:
        return {'quality': 'incomplete_session'}
    converted = []
    try:
        expected_ends = session_ends(record['date'])
        for bar, expected_end in zip(bars, expected_ends):
            timestamp = datetime.datetime.fromisoformat(bar['end'])
            if (timestamp.utcoffset() != datetime.timedelta(hours=8)
                    or timestamp.replace(tzinfo=None) != expected_end or bar.get('final') is not True):
                return {'quality': 'incomplete_session'}
            opening, high, low, close = (cents(bar[field]) for field in ('open', 'high', 'low', 'close'))
            volume = bar['volume_raw']
            if (high < max(opening, close) or low > min(opening, close) or low > high
                    or isinstance(volume, bool) or not isinstance(volume, (int, float))
                    or not math.isfinite(volume) or volume < 0):
                return {'quality': 'invalid_bar'}
            converted.append((opening, high, low, close))
    except (KeyError, TypeError, ValueError):
        return {'quality': 'invalid_bar'}
    opening = converted[0][0]
    high = max(bar[1] for bar in converted)
    low = min(bar[2] for bar in converted)
    close = converted[-1][3]
    if [opening, high, low, close] != record['daily_cents']:
        return {'quality': 'daily_ohlc_mismatch'}
    first_touch = next(index for index, bar in enumerate(converted) if bar[1] == high)
    return {'quality': 'matched', 'first_touch_bar_end': expected_ends[first_touch].strftime('%H:%M'),
            'first_touch_close_at_limit': converted[first_touch][3] == high,
            'first_touch_last_bar': first_touch == len(converted) - 1,
            'closed_at_limit': close == high}


def first_close_confirmation(path):
    return path['first_touch_close_at_limit'] and not path['first_touch_last_bar']


def confirmation_pf_bounds(confirmed_returns, unknown_returns):
    confirmed = np.asarray(confirmed_returns, dtype=float)
    unknown = np.asarray(unknown_returns, dtype=float)
    if confirmed.ndim != 1 or unknown.ndim != 1:
        raise ValueError('confirmation returns must be one-dimensional')
    if not np.all(np.isfinite(confirmed)) or not np.all(np.isfinite(unknown)):
        return {'available': False, 'reason': 'unresolved_returns'}
    confirmed_gain = float(np.maximum(confirmed, 0).sum())
    confirmed_loss = float(np.maximum(-confirmed, 0).sum())
    if confirmed_gain <= 0 or confirmed_loss <= 0:
        return {'available': False, 'reason': 'confirmed_gains_and_losses_required'}
    unknown_gain = float(np.maximum(unknown, 0).sum())
    unknown_loss = float(np.maximum(-unknown, 0).sum())
    return {'available': True, 'confirmed': len(confirmed), 'unknown': len(unknown),
            'unknown_wins': int(np.count_nonzero(unknown > 0)),
            'unknown_losses': int(np.count_nonzero(unknown < 0)),
            'unknown_flat': int(np.count_nonzero(unknown == 0)),
            'lower_pf': confirmed_gain / (confirmed_loss + unknown_loss),
            'upper_pf': (confirmed_gain + unknown_gain) / confirmed_loss}


def coverage_outcome_diagnostics(events, event_lookup, paths):
    diagnostics = {}
    groups = {'all_sampled': paths, 'unknown_quality': [item for item in paths if item['quality'] != 'matched']}
    for name, items in groups.items():
        mask = np.zeros(len(events['date']), dtype=bool)
        for item in items:
            mask[event_lookup[(item['code'], item['date'])]] = True
        diagnostics[name] = study.summarize(events, mask, 3, 0.002)
    return diagnostics


def mismatch_component_diagnostics(paths, responses):
    counts = {name: 0 for name in ('open', 'high', 'low', 'close')}
    details = []
    for item in paths:
        if item['quality'] != 'daily_ohlc_mismatch':
            continue
        key = (item['code'], item['date'])
        bars = responses[key]['archive']['bars']
        observed = [cents(bars[0]['open']), max(cents(bar['high']) for bar in bars),
                    min(cents(bar['low']) for bar in bars), cents(bars[-1]['close'])]
        delta = [actual - expected for actual, expected in zip(observed, item['daily_cents'])]
        for name, value in zip(counts, delta):
            counts[name] += int(value != 0)
        details.append({'code': item['code'], 'date': item['date'], 'delta_cents': delta})
    return {'mismatched_sessions': len(details), 'field_mismatch_counts': counts, 'details': details}


def read_responses(paths, expected):
    responses = {}
    for path in paths:
        with path.open(encoding='utf-8') as stream:
            for line in stream:
                response = json.loads(line)
                key = (response.get('code'), response.get('date'))
                if key not in expected or (key in responses and responses[key].get('status') == 'ok'):
                    raise ValueError('unexpected or duplicate successful minute response')
                responses[key] = response
    return responses


def validate_sample(sample):
    if (sample.get('schema_version') != 1 or sample.get('per_year') != minute_sample.SAMPLE_PER_YEAR
            or sample.get('method') != 'sha256-per-year-without-outcome' or sample.get('salt') != minute_sample.SAMPLE_SALT):
        raise ValueError('unsupported minute sample')
    records = sample['records']
    keys = [(record['code'], record['date']) for record in records]
    if (len(keys) != len(minute_sample.YEARS) * minute_sample.SAMPLE_PER_YEAR or len(set(keys)) != len(keys)
            or any(sum(date[:4] == year for _, date in keys) != minute_sample.SAMPLE_PER_YEAR
                   for year in minute_sample.YEARS)):
        raise ValueError('incomplete or invalid minute sample')
    return records, keys


def verify_daily_prices(connection, records):
    for record in records:
        prices = connection.execute(
            "SELECT open_micros,high_micros,low_micros,close_micros "
            "FROM daily_bar WHERE code=? AND trade_date=? AND adjust='none' AND data_field='day'",
            (record['code'], record['date'])).fetchone()
        if (prices is None or any(value is None or value % 10000 != 0 for value in prices)
                or [value // 10000 for value in prices] != record['daily_cents']):
            raise ValueError('sample OHLC does not match the frozen daily database')


def retry_sample(arguments):
    if arguments.retry_sample.exists():
        raise ValueError('refusing to overwrite retry sample')
    sample = json.loads(arguments.sample.read_text(encoding='utf-8'))
    records, keys = validate_sample(sample)
    responses = read_responses(arguments.minutes, set(keys))
    remaining = [record for record in records
                 if responses.get((record['code'], record['date']), {}).get('status') != 'ok']
    if not remaining:
        raise ValueError('no unsuccessful or missing requests')
    arguments.retry_sample.parent.mkdir(parents=True, exist_ok=True)
    with arguments.retry_sample.open('x', encoding='utf-8') as stream:
        json.dump({'schema_version': 1, 'records': remaining,
                   'original_sample_sha256': study.digest_file(arguments.sample)}, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps({'retry_requests': len(remaining)}, ensure_ascii=False))


def calculate(arguments):
    if arguments.result.exists():
        raise ValueError('refusing to overwrite minute path analysis')
    sample = json.loads(arguments.sample.read_text(encoding='utf-8'))
    records, keys = validate_sample(sample)
    if study.digest_file(arguments.events) != sample['events_sha256']:
        raise ValueError('event data differs from frozen sample')
    if study.digest_file(arguments.database) != sample['database_sha256']:
        raise ValueError('database differs from frozen sample')
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        verify_daily_prices(connection, records)
    finally:
        connection.close()
    events = study.load_events(arguments.events)
    base = study.select_mask(events, minute_sample.BASE_CONFIG)
    event_lookup = {}
    for index in np.flatnonzero(base & (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')):
        key = (str(events['code'][index]), str(events['date'][index]))
        if key in event_lookup:
            raise ValueError('duplicate frozen event')
        event_lookup[key] = int(index)
    if any(key not in event_lookup for key in keys):
        raise ValueError('sample event missing from frozen candidate base')
    responses = read_responses(arguments.minutes, set(keys))
    paths = []
    for record in records:
        key = (record['code'], record['date'])
        paths.append(dict(record, **classify_path(record, responses.get(key))))
    periods = {'training_2021_2023': set(minute_sample.YEARS[:3]), 'selection_2024': {'2024'}}
    periods.update({f'year_{year}': {year} for year in minute_sample.YEARS})
    groups = {}
    for name, years in periods.items():
        subset = [item for item in paths if item['date'][:4] in years]
        valid = [item for item in subset if item['quality'] == 'matched']
        conditions = {
            'all_matched': lambda item: True,
            'first_close_at_limit_before_1500': first_close_confirmation,
            'first_close_below_limit': lambda item: not item['first_touch_close_at_limit'],
            'closed_at_limit': lambda item: item['closed_at_limit'],
            'closed_below_limit': lambda item: not item['closed_at_limit'],
        }
        stats = {}
        for condition, predicate in conditions.items():
            mask = np.zeros(len(events['date']), dtype=bool)
            for item in valid:
                if predicate(item):
                    mask[event_lookup[(item['code'], item['date'])]] = True
            stats[condition] = study.summarize(events, mask, 3, 0.002)
            stats[condition]['day_cluster_ci'] = study.bootstrap_pf(events, mask, 3)
        confirmed_indices = [event_lookup[(item['code'], item['date'])]
                             for item in valid if first_close_confirmation(item)]
        unknown_indices = [event_lookup[(item['code'], item['date'])]
                           for item in subset if item['quality'] != 'matched']
        hold_index = study.HOLDS.index(3)
        confirmed_returns = study.net_returns(events['sale_ratio'][confirmed_indices, hold_index], 0.002)
        unknown_returns = study.net_returns(events['sale_ratio'][unknown_indices, hold_index], 0.002)
        groups[name] = {'sampled': len(subset), 'quality': dict(collections.Counter(item['quality'] for item in subset)),
                        'first_touch_last_bar': sum(item['first_touch_last_bar'] for item in valid),
                        'outcome_diagnostics': stats,
                        'coverage_outcome_diagnostics': coverage_outcome_diagnostics(events, event_lookup, subset),
                        'confirmation_pf_missingness_bounds': confirmation_pf_bounds(confirmed_returns, unknown_returns)}
    result = {'schema_version': 1, 'sample_sha256': study.digest_file(arguments.sample),
              'minutes_sha256': [study.digest_file(path) for path in arguments.minutes],
              'events_sha256': sample['events_sha256'],
              'downloaded_records': len(responses), 'sampled_records': len(records),
              'complete': all(item['quality'] == 'matched' for item in paths),
              'mismatch_components': mismatch_component_diagnostics(paths, responses), 'groups': groups,
              'paths': paths}
    arguments.result.parent.mkdir(parents=True, exist_ok=True)
    with arguments.result.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps({'downloaded_records': len(responses), 'groups': groups}, ensure_ascii=False, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sample', type=pathlib.Path, required=True)
    parser.add_argument('--minutes', type=pathlib.Path, nargs='+', required=True)
    parser.add_argument('--events', type=pathlib.Path)
    parser.add_argument('--database', type=pathlib.Path)
    output = parser.add_mutually_exclusive_group(required=True)
    output.add_argument('--result', type=pathlib.Path)
    output.add_argument('--retry-sample', type=pathlib.Path)
    arguments = parser.parse_args()
    if arguments.retry_sample:
        retry_sample(arguments)
    else:
        if not arguments.events or not arguments.database:
            parser.error('--result requires --events and --database')
        calculate(arguments)


if __name__ == '__main__':
    main()
