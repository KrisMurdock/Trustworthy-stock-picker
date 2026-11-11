#!/usr/bin/env python3
"""Check frozen first-board indicator classifications against THS observations."""

import argparse
import collections
import json
import pathlib
import re
import sqlite3

import numpy as np

import limit_up_account_sharpe as account
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_momentum_boll as candidate
import limit_up_study as study


NAMES = ('parent', 'macd_red', 'macd_growth', 'rsi_range', 'middle', 'upper', 'full')
SYMBOL = re.compile(r'^[0-9]{6}\.(SH|SZ)$')


def checked_codes(record):
    if (record.get('http_status') != 200 or record.get('rpc_error') or record.get('tool_error')
            or record.get('business_code') != 1):
        raise ValueError('THS request did not succeed')
    codes = record.get('codes')
    count = record.get('selected_count')
    if (type(count) is not int or count < 0 or not isinstance(codes, list)
            or any(not isinstance(code, str) or not SYMBOL.fullmatch(code) for code in codes)
            or len(set(codes)) != len(codes)):
        raise ValueError('invalid THS selection')
    if len(codes) != count:
        raise ValueError('THS selection is truncated or incomplete')
    return set(codes)


def selection_sets(records):
    if set(records) != set(NAMES):
        raise ValueError('missing or unexpected THS query')
    sets = {name: checked_codes(records[name]) for name in NAMES}
    parent = sets['parent']
    if any(not values <= parent for name, values in sets.items() if name != 'parent'):
        raise ValueError('THS filter returned a stock outside its parent')
    intersection = set.intersection(*(sets[name] for name in NAMES[1:-1]))
    return sets, intersection


def verify_dates(record, required, fields=()):
    if not record['selected_count']:
        return False
    answer = record.get('business_data', {}).get('answer', '')
    if not isinstance(answer, str):
        raise ValueError('THS response lacks table text')
    header = next((line for line in answer.splitlines() if line.startswith('|') and '股票代码' in line), '')
    if not header or any('[' + date.replace('-', '') + ']' not in header for date in required):
        raise ValueError('THS response contains missing or incorrect indicator dates')
    columns = {column.strip() for column in header.split('|')}
    if any(name + '[' + date.replace('-', '') + ']' not in columns for name, date in fields):
        raise ValueError('THS response contains missing or incorrectly dated fields')
    return True


def development_position(date, date_index):
    position = date_index.get(date, -1)
    if position < 2 or not candidate.FIRST_DATE <= date < candidate.END_DATE:
        raise ValueError('THS sample is not a development-period trading day')
    return position


def parent_fields(date, previous):
    return [('最高价:不复权', date), ('开盘价:不复权', date), ('涨停价', date),
            ('收盘价:不复权', previous), ('涨停价', previous)]


def indicator_fields(name, previous, earlier):
    macd = [('macd(macd值)', previous)]
    growth = macd + [('macd(macd值)', earlier)]
    rsi = [('rsi(rsi6值)', previous)]
    close = [('收盘价:前复权', previous)]
    middle = close + [('boll(mid值)', previous)]
    upper = close + [('boll(upper值)', previous)]
    return {'parent': [], 'macd_red': macd, 'macd_growth': growth, 'rsi_range': rsi,
            'middle': middle, 'upper': upper, 'full': growth + rsi + middle + upper}[name]


def compare(local, remote):
    if set(local) != set(remote):
        raise ValueError('comparison stock sets differ')
    false_positive = sorted(code for code in local if not local[code] and remote[code])
    false_negative = sorted(code for code in local if local[code] and not remote[code])
    return {'compared': len(local), 'local_selected': sum(local.values()),
            'ths_selected': sum(remote.values()),
            'different': len(false_positive) + len(false_negative),
            'ths_only_examples': false_positive[:8], 'local_only_examples': false_negative[:8]}


def run(arguments):
    if arguments.result.exists():
        raise ValueError('refusing to overwrite result')
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
        date = record.get('date')
        name = record.get('name')
        if not isinstance(date, str) or name in groups[date]:
            raise ValueError('duplicate or invalid THS observation')
        groups[date][name] = record
    if not groups:
        raise ValueError('no THS observations')
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if len(dates) < 100 or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('invalid frozen trading calendar')
    date_index = {str(date): index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    with np.load(arguments.input / 'events.npz', allow_pickle=False) as source:
        codes, event_dates = source['code'], source['date']
        known, prior = source['prior_reference_valid'], source['prior_limit']
    matching = np.isin(event_dates, list(groups)) & known & ~prior
    events = {(str(code), str(date)) for code, date in zip(codes[matching], event_dates[matching])}
    local_work = collections.defaultdict(list)
    result = {}
    remote_sets = {}
    for date, records in sorted(groups.items()):
        position = development_position(date, date_index)
        previous, earlier = str(dates[position - 1]), str(dates[position - 2])
        sets, intersection = selection_sets(records)
        unverified_dates = []
        for name in NAMES:
            required = [date, previous] if name == 'parent' else [previous]
            if name in ('macd_growth', 'full'):
                required.append(earlier)
            fields = parent_fields(date, previous) + indicator_fields(name, previous, earlier)
            if not verify_dates(records[name], required, fields):
                unverified_dates.append(name)
        result[date] = {'ths_parent': len(sets['parent']), 'ths_full': len(sets['full']),
                        'intersection': len(intersection),
                        'combined_difference': len(intersection ^ sets['full']),
                        'empty_result_dates_unverified': unverified_dates,
                        'missing_local_event': 0, 'comparisons': {}}
        remote_sets[date] = sets
        for code in sets['parent']:
            number, exchange = code.split('.')
            local = exchange.lower() + number
            if (local, date) in events:
                local_work[local].append((date, code))
            else:
                result[date]['missing_local_event'] += 1
    values = collections.defaultdict(lambda: {name: {} for name in NAMES[1:]})
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        for number, (code, samples) in enumerate(sorted(local_work.items()), 1):
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest or not path.exists():
                raise ValueError('frozen adjusted history is missing')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], str(dates[-1]))
            view = exits.price_view(raw, adjusted)
            features = indicators.calculate(adjusted, view['valid'])
            rsi = study.rsi6(adjusted[:, 3], view['valid'])
            middle, upper, _ = candidate.bollinger(adjusted[:, 3], view['valid'],
                                                    ddof=arguments.bollinger_ddof)
            for date, symbol in samples:
                position = date_index[date] - 1
                inputs = (features['macd'][position], features['macd'][position - 1], rsi[position],
                          adjusted[position, 3], middle[position], upper[position])
                if not np.isfinite(inputs).all():
                    continue
                macd, previous_macd, relative, close, mid, high = inputs
                tests = {'macd_red': macd > 0, 'macd_growth': macd > previous_macd,
                         'rsi_range': 50 <= relative < 75, 'middle': close >= mid, 'upper': close < high}
                tests['full'] = all(tests.values())
                for name, test in tests.items():
                    values[date][name][symbol] = bool(test)
            if number % 200 == 0 or number == len(local_work):
                print(f'Checked {number}/{len(local_work)} stocks', flush=True)
    finally:
        connection.close()
    for date in result:
        for name in NAMES[1:]:
            local = values[date][name]
            remote = {code: code in remote_sets[date][name] for code in local}
            result[date]['comparisons'][name] = compare(local, remote)
    payload = {'schema_version': 1, 'observations_sha256': study.digest_file(arguments.observations),
               'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
               'scope': 'intersecting historical first-board events; no PnL or fill validation',
               'bollinger_ddof': arguments.bollinger_ddof,
               'results': result}
    account.write_result(arguments.result, payload)
    print(json.dumps(payload, ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('database', 'calendar', 'qfq-dir', 'manifest', 'input', 'observations', 'result'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    parser.add_argument('--bollinger-ddof', type=int, choices=(0, 1), default=0)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
