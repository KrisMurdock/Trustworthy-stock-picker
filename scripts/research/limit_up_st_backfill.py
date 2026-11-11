#!/usr/bin/env python3
"""Backfill bounded development-only historical ST evidence from BaoStock."""

import argparse
import collections
import contextlib
import hashlib
import io
import json
import os
import pathlib
import socket
import time

import limit_up_minutes as minute_sample
import limit_up_st_analysis as analysis
import limit_up_study as study


SALT = 'limit-up-status-reconcile-v1'


def ordered_groups(candidate_keys, successful_keys):
    groups = collections.defaultdict(list)
    for code, date in sorted(candidate_keys - successful_keys):
        groups[(code, date[:4])].append(date)
    ordered = sorted(groups, key=lambda key: hashlib.sha256((SALT + key[0] + key[1]).encode('ascii')).digest())
    return [(code, groups[(code, year)]) for code, year in ordered]


def fetch_group(client, code, dates):
    if not dates or len({date[:4] for date in dates}) != 1:
        raise ValueError('status query must contain one nonempty stock-year')
    provider_code = code[:2] + '.' + code[2:]
    result = {date: {'status': 'unknown', 'reason': 'source_unavailable'} for date in dates}
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            login = client.login()
            if login.error_code != '0':
                return result
            try:
                response = client.query_history_k_data_plus(
                    provider_code, 'date,code,isST,tradestatus', start_date=min(dates),
                    end_date=max(dates), frequency='d', adjustflag='3')
                if response.error_code != '0':
                    return result
                rows = []
                while response.next():
                    if len(rows) >= 252:
                        raise ValueError('year query exceeds bounded session count')
                    rows.append(dict(zip(response.fields, response.get_row_data())))
                if response.error_code != '0':
                    return result
            finally:
                client.logout()
        grouped = collections.defaultdict(list)
        for row in rows:
            if row.get('date') in result:
                grouped[row['date']].append(row)
        for date in dates:
            entries = grouped[date]
            if (len(entries) == 1 and entries[0].get('code') == provider_code
                    and entries[0].get('tradestatus') == '1' and entries[0].get('isST') in ('0', '1')):
                result[date] = {'status': 'ok', 'is_st': entries[0]['isST'] == '1'}
            else:
                result[date] = {'status': 'unknown', 'reason': 'missing_or_invalid_status'}
    except (OSError, ValueError, KeyError, AttributeError, TypeError):
        return result
    return result


def run(arguments, client):
    if arguments.output.exists():
        raise ValueError('refusing to overwrite status evidence')
    if arguments.max_groups < 1 or not 1 <= arguments.delay_seconds <= 60:
        raise ValueError('invalid request bound or delay')
    candidate = json.loads(arguments.candidates.read_text(encoding='utf-8'))
    sample = json.loads(arguments.sample.read_text(encoding='utf-8'))
    if (candidate.get('schema_version') != 1 or sample.get('schema_version') != 1
            or candidate.get('events_sha256') != sample.get('events_sha256')):
        raise ValueError('candidate and sample provenance differ')
    records = analysis.event_records(candidate['records'])
    expected_sample, available = minute_sample.sample_records(candidate['records'])
    if sample.get('records') != expected_sample or sample.get('available_by_year') != available:
        raise ValueError('sample does not match frozen candidates')
    sample_keys = set(analysis.event_records(sample['records']))
    candidate_sha = study.digest_file(arguments.candidates)
    sample_sha = study.digest_file(arguments.sample)
    successes, _ = analysis.read_statuses(arguments.previous, records, candidate_sha, sample_keys, sample_sha)
    groups = ordered_groups(set(records), set(successes))
    print(f'已有有效状态 {len(successes)} 笔；剩余 {len(groups)} 个股票年度组', flush=True)
    socket.setdefaulttimeout(10)
    failed_in_a_row = 0
    completed = 0
    processed = 0
    with arguments.output.open('x', encoding='utf-8') as stream:
        os.chmod(arguments.output, 0o600)
        for index, (code, dates) in enumerate(groups[:arguments.max_groups], start=1):
            statuses = fetch_group(client, code, dates)
            failed = False
            for date in dates:
                entry = {'code': code, 'date': date, 'candidate_sha256': candidate_sha, **statuses[date]}
                stream.write(json.dumps(entry, ensure_ascii=False) + '\n')
                if entry['status'] == 'ok':
                    completed += 1
                else:
                    failed = True
            stream.flush()
            processed = index
            failed_in_a_row = failed_in_a_row + 1 if failed else 0
            if index % 25 == 0 or failed:
                print(f'已处理 {index}/{len(groups)} 组，新增有效状态 {completed} 笔', flush=True)
            if failed_in_a_row >= 3:
                print('连续三组未完整返回；已保留部分结果以便重试', flush=True)
                break
            if index < len(groups) and index < arguments.max_groups:
                time.sleep(arguments.delay_seconds)
    return 0 if processed == len(groups) and len(successes) + completed == len(records) else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidates', type=pathlib.Path, required=True)
    parser.add_argument('--sample', type=pathlib.Path, required=True)
    parser.add_argument('--previous', type=pathlib.Path, nargs='*', default=[])
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--max-groups', type=int, default=1000)
    parser.add_argument('--delay-seconds', type=float, default=1)
    arguments = parser.parse_args()
    import baostock
    return run(arguments, baostock)


if __name__ == '__main__':
    raise SystemExit(main())
