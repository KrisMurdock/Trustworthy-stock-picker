#!/usr/bin/env python3
"""Audit historical ST coverage and compare locked candidate filters."""

import argparse
import datetime
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_dynamic_exits as dynamic
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_minutes as minute_sample
import limit_up_study as study
import limit_up_volume as volume


FILTER_NAMES = (
    'none', 'prior_close_not_limit', 'prior_close_above_ma20',
    'prior_volume_gt_ma5', 'prior_volume_le_ma5', 'prior_macd_red',
    'prior_rsi_ge_50', 'prior_k_ge_70', 'prior_atr_2_to_6_pct', 'prior_close_up',
)

EXIT_NAMES = (
    'fixed_t1', 'fixed_t3', 'weak_zero_t2_else_t3', 'broken_t1_else_t3',
    'below_97_t1_else_t3', 'below_94_t1_else_t3', 'below_90_t1_else_t3',
    'broken_t1_else_weak_zero',
)


def event_records(records):
    result = {}
    for record in records:
        key = (record.get('code'), record.get('date'))
        if key in result or not isinstance(key[0], str) or not isinstance(key[1], str):
            raise ValueError('duplicate or invalid candidate event')
        try:
            date = datetime.date.fromisoformat(key[1])
        except ValueError as error:
            raise ValueError('invalid candidate date') from error
        if (date.isoformat() != key[1] or key[1][:4] not in minute_sample.YEARS
                or len(key[0]) != 8 or not key[0][2:].isdigit()
                or not key[0].startswith(('sh688', 'sz300', 'sz301', 'sz302'))):
            raise ValueError('candidate must be a development-period event')
        prices = record.get('daily_cents')
        if (not isinstance(prices, list) or len(prices) != 4
                or any(type(price) is not int or price <= 0 for price in prices)):
            raise ValueError('invalid frozen daily prices')
        result[key] = record
    return result


def read_statuses(paths, candidates, candidate_sha, sample_keys, sample_sha):
    statuses = {}
    unknown = set()
    for path in paths:
        with path.open(encoding='utf-8') as stream:
            for line in stream:
                item = json.loads(line)
                key = (item.get('code'), item.get('date'))
                if key not in candidates:
                    raise ValueError('status refers to an unexpected event')
                candidate_proof = 'candidate_sha256' in item
                sample_proof = 'sample_sha256' in item
                if (candidate_proof == sample_proof
                        or (candidate_proof and item['candidate_sha256'] != candidate_sha)
                        or (sample_proof and (item['sample_sha256'] != sample_sha or key not in sample_keys))):
                    raise ValueError('status has missing, ambiguous or mismatched provenance')
                status = item.get('status')
                if status == 'ok':
                    value = item.get('is_st')
                    if type(value) is not bool or key in statuses:
                        raise ValueError('duplicate or invalid successful status')
                    statuses[key] = value
                elif status == 'unknown':
                    if 'is_st' in item:
                        raise ValueError('unknown status must not contain an ST value')
                    unknown.add(key)
                else:
                    raise ValueError('invalid status')
    return statuses, unknown - set(statuses)


def filter_masks(events, features):
    count = len(events['date'])
    result = {'none': (np.ones(count, dtype=bool), np.ones(count, dtype=bool))}
    reference = events['prior_reference_valid']
    result['prior_close_not_limit'] = (reference & ~events['prior_limit'], reference)
    result['prior_close_above_ma20'] = (events['above_ma20'], np.ones(count, dtype=bool))
    for name, values, predicate in (
        ('prior_volume_gt_ma5', features['volume_ratio'], lambda value: value > 1),
        ('prior_volume_le_ma5', features['volume_ratio'], lambda value: value <= 1),
        ('prior_macd_red', features['macd'], lambda value: value > 0),
        ('prior_rsi_ge_50', events['rsi6'], lambda value: value >= 50),
        ('prior_k_ge_70', features['kdj_k'], lambda value: value >= 70),
        ('prior_atr_2_to_6_pct', features['atr_ratio'], lambda value: (value >= 0.02) & (value < 0.06)),
    ):
        known = np.isfinite(values)
        result[name] = (known & predicate(values), known)
    known = features['close_known']
    result['prior_close_up'] = (known & features['close_up'], known)
    if tuple(result) != FILTER_NAMES:
        raise ValueError('filter order differs from the locked contract')
    return result


def prior_limit_relation(close_cents, reference_cents, prior_limit):
    if close_cents <= 0 or reference_cents <= 0:
        raise ValueError('invalid prior-close reference')
    upper = int(study.limit_cents(reference_cents, 20))
    if prior_limit != (close_cents == upper):
        raise ValueError('prior-close limit classification mismatch')
    if close_cents < upper:
        return -1
    if close_cents > upper:
        return 1
    return 0


def features_before_signal(position, prior_indicators, prior_volume):
    if position <= 0:
        raise ValueError('event lacks a previous trading session')
    features = {name: prior_indicators[name][position - 1]
                for name in ('dmi_difference', 'macd', 'kdj_k', 'atr_ratio')}
    features.update({name: prior_volume[name][position - 1]
                     for name in ('volume_ratio', 'close_known', 'close_up')})
    return features


def period_masks(dates):
    years = np.asarray([int(date[:4]) for date in dates], dtype=np.int16)
    result = {'training_2021_2023': (years >= 2021) & (years <= 2023),
              'selection_2024': years == 2024}
    result.update({f'year_{year}': years == year for year in range(2021, 2025)})
    return result


def summarize_periods(events, mask, periods):
    result = {}
    for name, period in periods.items():
        selected = mask & period
        result[name] = {'10bp': study.summarize(events, selected, 3, 0.001),
                        '20bp': study.summarize(events, selected, 3, 0.002),
                        '20bp_day_cluster_ci': study.bootstrap_pf(events, selected, 3)}
    return result


def verify_dynamic_baseline(actual, reference):
    records = [record for record in reference['results'] if record['policy'] == 'weak_zero_t2_else_t3']
    if len(records) != 1:
        raise ValueError('locked dynamic baseline is unavailable')
    for name, values in actual.items():
        for cost in ('10bp', '20bp'):
            expected = records[0]['periods'][name][cost]
            observed = values[cost]
            for key in ('signals', 'closed', 'unresolved', 'days', 'stocks', 'wins', 'losses', 'delayed'):
                if observed[key] != expected[key]:
                    raise ValueError('locked dynamic baseline count mismatch')
            for key in ('win_rate', 'mean_return', 'payoff', 'pf'):
                left, right = observed[key], expected[key]
                if (left is None) != (right is None) or (left is not None and not np.isclose(left, right, rtol=0, atol=1e-12)):
                    raise ValueError('locked dynamic baseline return mismatch')


def evaluate_grid(events, masks, eligible_events, periods):
    records = []
    for filter_name in FILTER_NAMES:
        accepted, known = masks[filter_name]
        selected = eligible_events & accepted
        excluded = eligible_events & known & ~accepted
        for exit_name, replay in events.items():
            metrics = summarize_periods(replay, selected, periods)
            training = metrics['training_2021_2023']['20bp']
            selection = metrics['selection_2024']['20bp']
            sample_ok = study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)
            score = study.candidate_score(training, selection) if sample_ok else None
            records.append({
                'filter': filter_name, 'exit': exit_name,
                'missing_filter_events': int(np.count_nonzero(eligible_events & ~known)),
                'sample_ok': sample_ok, 'score': score,
                'targets_passed': bool(sample_ok and score >= 1 and training['mean_return'] > 0
                                       and selection['mean_return'] > 0),
                'periods': metrics,
                'excluded_periods': {name: study.summarize(replay, excluded & period, 3, 0.002)
                                     for name, period in periods.items()},
            })
    return records


def closing_exit_holds(close_cents, entry_cents, dynamic_holds):
    close = np.asarray(close_cents, dtype=np.int64)
    entry = np.asarray(entry_cents, dtype=np.int64)
    base = np.asarray(dynamic_holds)
    if (close.ndim != 1 or entry.shape != close.shape or base.shape != close.shape
            or np.any(~np.isin(base, (2, 3)))):
        raise ValueError('invalid close-exit inputs')
    known = (close > 0) & (entry > 0)
    if np.any((close > 0) != (entry > 0)) or np.any(known & (close > entry)):
        raise ValueError('invalid close or limit price')
    broken = known & (close < entry)
    result = {
        'fixed_t1': np.ones(len(close), dtype=np.int8),
        'fixed_t3': np.full(len(close), 3, dtype=np.int8),
        'weak_zero_t2_else_t3': base.copy(),
        'broken_t1_else_t3': np.where(broken, 1, 3).astype(np.int8),
    }
    for threshold in (97, 94, 90):
        weak = known & (close * 100 < entry * threshold)
        result[f'below_{threshold}_t1_else_t3'] = np.where(weak, 1, 3).astype(np.int8)
    result['broken_t1_else_weak_zero'] = np.where(broken, 1, base).astype(np.int8)
    if tuple(result) != EXIT_NAMES:
        raise ValueError('close-exit order differs from the locked contract')
    return result


def evaluate_closing_exits(events, policies, eligible_events, periods):
    results = []
    for name, holds in policies.items():
        replay = dynamic.apply_holds(events, holds)
        metrics = summarize_periods(replay, eligible_events, periods)
        training = metrics['training_2021_2023']['20bp']
        selection = metrics['selection_2024']['20bp']
        sample_ok = study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)
        score = study.candidate_score(training, selection) if sample_ok else None
        results.append({
            'policy': name, 'sample_ok': sample_ok, 'score': score,
            'targets_passed': bool(sample_ok and score >= 1 and training['mean_return'] > 0
                                   and selection['mean_return'] > 0),
            'periods': metrics,
            'decisions': {period: {'t1': int(np.count_nonzero(eligible_events & mask & (holds == 1))),
                                   't2': int(np.count_nonzero(eligible_events & mask & (holds == 2))),
                                   't3': int(np.count_nonzero(eligible_events & mask & (holds == 3)))}
                          for period, mask in periods.items()},
            'comparison_with_fixed_t3': {period: dynamic.compare_with_fixed(events, replay, holds,
                                                                             eligible_events & mask)
                                         for period, mask in periods.items()},
        })
    return results


def locked_candidate_diagnostics(events, candidate, holds):
    candidate = np.asarray(candidate, dtype=bool)
    holds = np.asarray(holds)
    dates = events['date']
    if (candidate.shape != dates.shape or holds.shape != dates.shape
            or np.any(candidate & ~np.isin(holds, (2, 3)))):
        raise ValueError('invalid locked candidate diagnostics inputs')
    if np.any(candidate & ((dates < '2021-01-01') | (dates >= '2025-01-01'))):
        raise ValueError('diagnostics must not examine holdout signals')

    periods = period_masks(dates)
    groups = {
        'all': candidate,
        'sealed': candidate & events['closed_at_limit'],
        'broken': candidate & ~events['closed_at_limit'],
        'planned_t2': candidate & (holds == 2),
        'planned_t3': candidate & (holds == 3),
    }
    summaries = {
        name: {period: study.summarize(events, mask & periods[period], 3, 0.002)
               for period in ('training_2021_2023', 'selection_2024')}
        for name, mask in groups.items()
    }
    september = (dates >= '2024-09-01') & (dates < '2024-10-01')
    summaries['september_2024'] = {
        'selection_2024': study.summarize(events, candidate & september, 3, 0.002),
    }
    summaries['excluding_september_2024'] = {
        'selection_2024': study.summarize(events, candidate & periods['selection_2024'] & ~september, 3, 0.002),
    }
    risk = {}
    hold_index = study.HOLDS.index(3)
    for name in ('training_2021_2023', 'selection_2024'):
        ratios = events['sale_ratio'][candidate & periods[name], hold_index]
        returns = study.net_returns(ratios[np.isfinite(ratios)], 0.002)
        risk[name] = {
            'closed': int(len(returns)),
            'return_p05': float(np.quantile(returns, 0.05)) if len(returns) else None,
            'return_p50': float(np.quantile(returns, 0.50)) if len(returns) else None,
            'loss_at_least_5pct': int(np.count_nonzero(returns <= -0.05)),
            'loss_at_least_10pct': int(np.count_nonzero(returns <= -0.10)),
        }
    return {'summaries_20bp': summaries, 'risk_20bp': risk}


def run(arguments):
    started = time.monotonic()
    output = arguments.output
    if arguments.result.exists():
        raise ValueError('refusing to overwrite ST analysis result')
    metadata = json.loads((output / 'extract.json').read_text(encoding='utf-8'))
    selection_path = output / 'selection.json'
    indicator_path = output / 'indicator-development.json'
    dynamic_path = output / 'dynamic-exit-development.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    indicator = json.loads(indicator_path.read_text(encoding='utf-8'))
    reference = json.loads(dynamic_path.read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata.get('limited') or not metadata.get('database_sha256')):
        raise ValueError('analysis requires a full verified extraction')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (output / 'events.npz', metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    if (selection['events_sha256'] != metadata['events_sha256']
            or indicator['events_sha256'] != metadata['events_sha256']
            or indicator['selection_sha256'] != study.digest_file(selection_path)
            or indicator['base'] != minute_sample.BASE_CONFIG
            or reference['events_sha256'] != metadata['events_sha256']
            or reference['database_sha256'] != metadata['database_sha256']
            or reference['indicator_sha256'] != study.digest_file(indicator_path)
            or reference['base'] != minute_sample.BASE_CONFIG
            or reference['entry_filter'] != 'pdi_gt_mdi'
            or reference.get('holdout_signals_examined') is not False):
        raise ValueError('locked development artifacts mismatch')
    candidate = json.loads(arguments.candidates.read_text(encoding='utf-8'))
    sample = json.loads(arguments.sample.read_text(encoding='utf-8'))
    if (candidate.get('schema_version') != 1 or candidate.get('events_sha256') != metadata['events_sha256']
            or sample.get('schema_version') != 1 or sample.get('events_sha256') != metadata['events_sha256']
            or sample.get('database_sha256') != metadata['database_sha256']
            or sample.get('indicator_sha256') != study.digest_file(indicator_path)):
        raise ValueError('candidate or sample provenance mismatch')
    candidate_records = event_records(candidate['records'])
    expected_sample, available = minute_sample.sample_records(candidate['records'])
    if (sample.get('records') != expected_sample or sample.get('available_by_year') != available
            or len(candidate_records) != reference['candidate_development_events']):
        raise ValueError('candidate or fixed sample does not match locked development counts')
    sample_keys = set(event_records(sample['records']))
    candidate_sha = study.digest_file(arguments.candidates)
    sample_sha = study.digest_file(arguments.sample)
    status_digests = [study.digest_file(path) for path in arguments.statuses]
    statuses, explicitly_unknown = read_statuses(arguments.statuses, candidate_records, candidate_sha, sample_keys, sample_sha)
    events = study.load_events(output / 'events.npz')
    selected_indices = dynamic.development_indices(events)
    selected = {name: values[selected_indices] for name, values in events.items()}
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    date_index = {date: index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    count = len(selected_indices)
    flags = np.zeros(count, dtype=bool)
    close_ratios = np.full(count, np.nan)
    signal_close_cents = np.zeros(count, dtype=np.int64)
    entry_cents = np.zeros(count, dtype=np.int64)
    prior_relations = np.full(count, 2, dtype=np.int8)
    features = {name: np.full(count, np.nan) for name in ('macd', 'kdj_k', 'atr_ratio', 'volume_ratio')}
    features.update({name: np.zeros(count, dtype=bool) for name in ('close_known', 'close_up')})
    seen = set()
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
                date = str(selected['date'][index])
                position = date_index[date]
                prior = features_before_signal(position, previous_indicators, previous_volume)
                dmi = prior['dmi_difference']
                if not np.isfinite(dmi) or dmi <= 0:
                    continue
                key = (code, date)
                if key in seen or key not in candidate_records:
                    raise ValueError('candidate event set differs from locked replay')
                seen.add(key)
                prices = raw[position, :4]
                if np.any(prices % 10000 != 0) or (prices // 10000).tolist() != candidate_records[key]['daily_cents']:
                    raise ValueError('candidate daily prices differ from frozen input')
                upper = study.limit_cents(view['previous_close'][position], 20)
                entry = upper * view['adj_open'][position] / view['raw_open'][position]
                if selected['prior_reference_valid'][index]:
                    previous_close = raw[position - 1, 3]
                    if previous_close % 10000 != 0:
                        raise ValueError('prior closing price is not in integer cents')
                    prior_relations[index] = prior_limit_relation(
                        int(previous_close // 10000), int(view['previous_close'][position - 1]),
                        bool(selected['prior_limit'][index]))
                for column, hold in enumerate(study.HOLDS):
                    actual = exits.fixed_open_ratio(view, position, entry, hold)
                    expected = selected['sale_ratio'][index, column]
                    if not np.isclose(actual, expected, rtol=0, atol=1e-10, equal_nan=True):
                        raise ValueError('fixed exit differs from frozen event')
                flags[index] = True
                close_ratios[index] = dynamic.next_close_ratio(view, raw, position, upper)
                signal_close_cents[index] = prices[3] // 10000
                entry_cents[index] = upper
                if bool(selected['closed_at_limit'][index]) != (signal_close_cents[index] == upper):
                    raise ValueError('signal closing price differs from frozen event')
                for name in features:
                    features[name][index] = prior[name]
            if number % 100 == 0 or number == len(codes):
                print(f'已核对 {number}/{len(codes)} 只开发期股票', flush=True)
    finally:
        connection.close()
    if seen != set(candidate_records):
        raise ValueError('candidate coverage differs from locked replay')
    periods = period_masks(selected['date'])
    dynamic.verify_baseline(selected, flags, periods, indicator)
    holds = dynamic.choose_holds(close_ratios, dynamic.POLICIES[1])
    replay = dynamic.apply_holds(selected, holds)
    original = summarize_periods(replay, flags, periods)
    verify_dynamic_baseline(original, reference)
    known_non_st = np.zeros(count, dtype=bool)
    known_st = np.zeros(count, dtype=bool)
    for index in np.flatnonzero(flags):
        key = (str(selected['code'][index]), str(selected['date'][index]))
        if key in statuses:
            known_st[index] = statuses[key]
            known_non_st[index] = not statuses[key]
    unknown = flags & ~(known_st | known_non_st)
    status_masks = {'all': flags, 'known_non_st': known_non_st, 'known_st': known_st, 'unknown': unknown}
    coverage = {name: {year: int(np.count_nonzero(mask & period)) for year, period in periods.items()}
                for name, mask in status_masks.items()}
    prior_close_comparison = {
        name: {label: int(np.count_nonzero(mask & (prior_relations == relation)))
               for label, relation in (('below', -1), ('equal', 0), ('above', 1), ('unknown_reference', 2))}
        for name, mask in status_masks.items()
    }
    complete = not np.any(unknown)
    diagnostics = {name: summarize_periods(replay, mask, periods) for name, mask in status_masks.items()}
    experiments = []
    closing_experiments = []
    closing_diagnostics = {}
    candidate_diagnostics = {}
    if complete:
        masks = filter_masks(selected, features)
        experiments = evaluate_grid({'fixed_t3': selected, 'weak_zero_t2_else_t3': replay},
                                    masks, known_non_st, periods)
        closing_policies = closing_exit_holds(signal_close_cents, entry_cents, holds)
        closing_experiments = evaluate_closing_exits(selected, closing_policies, known_non_st, periods)
        closing_diagnostics = {
            'sealed': summarize_periods(replay, known_non_st & selected['closed_at_limit'], periods),
            'broken': summarize_periods(replay, known_non_st & ~selected['closed_at_limit'], periods),
        }
        candidate_diagnostics = locked_candidate_diagnostics(
            replay, known_non_st & masks['prior_close_not_limit'][0], holds)
    if [study.digest_file(path) for path in arguments.statuses] != status_digests:
        raise ValueError('status evidence changed during analysis')
    result = {
        'schema_version': 1, 'events_sha256': metadata['events_sha256'],
        'database_sha256': metadata['database_sha256'], 'candidate_sha256': candidate_sha,
        'sample_sha256': sample_sha, 'dynamic_baseline_sha256': study.digest_file(dynamic_path),
        'status_sha256': status_digests,
        'status_provider': 'BaoStock historical isST; not independently verified for all events',
        'candidate_count': len(candidate_records), 'status_success_count': len(statuses),
        'explicitly_unknown_count': len(explicitly_unknown), 'complete_candidate_coverage': complete,
        'coverage': coverage, 'status_diagnostics': diagnostics,
        'prior_close_comparison': prior_close_comparison,
        'tested_combinations': len(experiments),
        'sample_eligible_combinations': sum(record['sample_ok'] for record in experiments),
        'targets_passed_count': sum(record['targets_passed'] for record in experiments),
        'tested_closing_exit_policies': len(closing_experiments),
        'closing_exit_targets_passed_count': sum(record['targets_passed'] for record in closing_experiments),
        'closing_diagnostics': closing_diagnostics, 'closing_exit_results': closing_experiments,
        'locked_candidate_diagnostics': candidate_diagnostics,
        'holdout_signals_examined': False, 'results': experiments,
        'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    with arguments.result.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')
    print(json.dumps({key: result[key] for key in ('candidate_count', 'status_success_count',
                     'complete_candidate_coverage', 'tested_combinations',
                     'sample_eligible_combinations', 'targets_passed_count',
                     'tested_closing_exit_policies', 'closing_exit_targets_passed_count')}, ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=pathlib.Path, required=True)
    parser.add_argument('--calendar', type=pathlib.Path, required=True)
    parser.add_argument('--qfq-dir', type=pathlib.Path, required=True)
    parser.add_argument('--manifest', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--candidates', type=pathlib.Path, required=True)
    parser.add_argument('--sample', type=pathlib.Path, required=True)
    parser.add_argument('--statuses', type=pathlib.Path, nargs='+', required=True)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
