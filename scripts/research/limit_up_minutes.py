#!/usr/bin/env python3
"""Select and inspect a development-only sample of limit-up minute paths."""

import argparse
import hashlib
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_study as study


YEARS = ('2021', '2022', '2023', '2024')
SAMPLE_PER_YEAR = 25
SAMPLE_SALT = 'limit-up-minute-path-v1'
BASE_CONFIG = {'board': 20, 'prior': 'any', 'trend': 'ma20_above_ma60',
               'rsi': 'any', 'gap': 'three_to_seven', 'hold': 3}


def sample_records(records, per_year=SAMPLE_PER_YEAR):
    if per_year <= 0:
        raise ValueError('sample size must be positive')
    identifiers = [(record['code'], record['date']) for record in records]
    if len(set(identifiers)) != len(identifiers):
        raise ValueError('duplicate candidate event')
    if any(date[:4] not in YEARS for _, date in identifiers):
        raise ValueError('sample selection is restricted to development years')
    sample = []
    available = {}
    for year in YEARS:
        candidates = [record for record in records if record['date'][:4] == year]
        available[year] = len(candidates)
        if len(candidates) < per_year:
            raise ValueError(f'insufficient candidates for {year}')
        candidates.sort(key=lambda record: hashlib.sha256(
            f"{SAMPLE_SALT}|{record['date']}|{record['code']}".encode('ascii')).digest())
        sample.extend(candidates[:per_year])
    return sorted(sample, key=lambda record: (record['date'], record['code'])), available


def prepare(arguments):
    candidates_path = getattr(arguments, 'candidates', None)
    if (arguments.sample.exists() or (candidates_path is not None and candidates_path.exists())
            or candidates_path == arguments.sample):
        raise ValueError('refusing to overwrite minute sample')
    metadata = json.loads((arguments.output / 'extract.json').read_text(encoding='utf-8'))
    selection_path = arguments.output / 'selection.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    indicator = json.loads((arguments.output / 'indicator-development.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata['limited'] or not metadata['database_sha256']):
        raise ValueError('minute sample requires a full verified extraction')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (arguments.output / 'events.npz', metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    if (selection['events_sha256'] != metadata['events_sha256']
            or indicator['events_sha256'] != metadata['events_sha256']
            or indicator['selection_sha256'] != study.digest_file(selection_path)):
        raise ValueError('development artifacts do not match the frozen inputs')
    indicator_result = [record for record in indicator['results']
                        if record['filter'] == 'pdi_gt_mdi' and record['hold'] == 3]
    if len(indicator_result) != 1 or indicator['base'] != BASE_CONFIG:
        raise ValueError('minute sample requires the locked LB-R003 candidate')
    events = study.load_events(arguments.output / 'events.npz')
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    selected = np.flatnonzero(study.select_mask(events, BASE_CONFIG) & development)
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    date_index = {date: index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    records = []
    codes = np.unique(events['code'][selected])
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
            dmi = indicators.calculate(adjusted, valid)['dmi_difference']
            for event_index in selected[events['code'][selected] == code]:
                date = str(events['date'][event_index])
                position = date_index[date]
                if position <= 0 or not np.isfinite(dmi[position - 1]) or dmi[position - 1] <= 0:
                    continue
                prices = raw[position, :4]
                if np.any(prices % 10000 != 0):
                    raise ValueError('raw prices are not whole cents')
                records.append({'code': code, 'date': date,
                                'daily_cents': [int(price // 10000) for price in prices]})
            if code_number % 100 == 0 or code_number == len(codes):
                print(f'已核对 {code_number}/{len(codes)} 只开发期股票', flush=True)
    finally:
        connection.close()
    sample, available = sample_records(records)
    periods = indicator_result[0]['periods']
    if (sum(available[year] for year in YEARS[:3]) != periods['training_2021_2023']['20bp']['signals']
            or available['2024'] != periods['selection_2024']['20bp']['signals']):
        raise ValueError('minute sample candidate differs from LB-R003 development results')
    result = {'schema_version': 1, 'method': 'sha256-per-year-without-outcome', 'salt': SAMPLE_SALT,
              'per_year': SAMPLE_PER_YEAR, 'available_by_year': available,
              'events_sha256': metadata['events_sha256'], 'database_sha256': metadata['database_sha256'],
              'indicator_sha256': study.digest_file(arguments.output / 'indicator-development.json'),
              'records': sample, 'elapsed_seconds': round(time.monotonic() - started, 2)}
    arguments.sample.parent.mkdir(parents=True, exist_ok=True)
    with arguments.sample.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    if candidates_path is not None:
        export_candidates(candidates_path, records, metadata['events_sha256'])
    print(json.dumps({'available_by_year': available, 'sampled': len(sample)}, ensure_ascii=False), flush=True)


def export_candidates(path, records, events_sha):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x', encoding='utf-8') as stream:
        json.dump({'schema_version': 1, 'events_sha256': events_sha, 'records': records},
                  stream, ensure_ascii=False, indent=2)
        stream.write('\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=pathlib.Path, required=True)
    parser.add_argument('--calendar', type=pathlib.Path, required=True)
    parser.add_argument('--qfq-dir', type=pathlib.Path, required=True)
    parser.add_argument('--manifest', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--sample', type=pathlib.Path, required=True)
    parser.add_argument('--candidates', type=pathlib.Path)
    prepare(parser.parse_args())


if __name__ == '__main__':
    main()
