#!/usr/bin/env python3
"""Compare pre-entry filters by realized net wins in frozen development data."""

import argparse
import json
import pathlib
import re
import sqlite3
import time

import numpy as np

import limit_up_dynamic_exits as dynamic
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_main_board as main_board
import limit_up_minutes as minute_sample
import limit_up_st_analysis as analysis
import limit_up_study as study
import limit_up_volume as volume


FILTER_NAMES = (
    'none', 'prior_close_not_limit', 'prior_close_above_ma20',
    'prior_volume_gt_ma5', 'prior_volume_le_ma5', 'prior_macd_red',
    'prior_rsi_ge_50', 'prior_rsi_ge_60', 'prior_rsi_60_to_80', 'prior_rsi_70_to_80',
    'prior_k_ge_50', 'prior_k_ge_70', 'prior_atr_2_to_6_pct', 'prior_close_up',
    'prior_pdi_gt_mdi',
)
NUMERIC_FEATURES = ('rsi6', 'gap', 'kdj_k', 'atr_ratio', 'volume_ratio', 'dmi_difference')
PRIMARY_PERIODS = ('training_2021_2023', 'selection_2024')


def development_indices(events):
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    growth = study.select_mask(events, minute_sample.BASE_CONFIG)
    main = ((events['board'] == 10) & events['ma20_above_ma60']
            & (events['gap'] >= 0) & (events['gap'] < 0.07))
    return np.flatnonzero(development & (growth | main))


def filter_masks(signals, features):
    count = len(signals['date'])
    known_all = np.ones(count, dtype=bool)
    reference = signals['prior_reference_valid']
    result = {
        'none': (known_all, known_all),
        'prior_close_not_limit': (reference & ~signals['prior_limit'], reference),
        'prior_close_above_ma20': (signals['above_ma20'], known_all),
    }
    for name, values, predicate in (
        ('prior_volume_gt_ma5', features['volume_ratio'], lambda value: value > 1),
        ('prior_volume_le_ma5', features['volume_ratio'], lambda value: value <= 1),
        ('prior_macd_red', features['macd'], lambda value: value > 0),
        ('prior_rsi_ge_50', signals['rsi6'], lambda value: value >= 50),
        ('prior_rsi_ge_60', signals['rsi6'], lambda value: value >= 60),
        ('prior_rsi_60_to_80', signals['rsi6'], lambda value: (value >= 60) & (value < 80)),
        ('prior_rsi_70_to_80', signals['rsi6'], lambda value: (value >= 70) & (value < 80)),
        ('prior_k_ge_50', features['kdj_k'], lambda value: value >= 50),
        ('prior_k_ge_70', features['kdj_k'], lambda value: value >= 70),
        ('prior_atr_2_to_6_pct', features['atr_ratio'], lambda value: (value >= 0.02) & (value < 0.06)),
    ):
        known = np.isfinite(values)
        result[name] = (known & predicate(values), known)
    result['prior_close_up'] = (features['close_known'] & features['close_up'], features['close_known'])
    dmi_known = np.isfinite(features['dmi_difference'])
    result['prior_pdi_gt_mdi'] = (dmi_known & (features['dmi_difference'] > 0), dmi_known)
    if tuple(result) != FILTER_NAMES:
        raise ValueError('filters differ from the locked win-analysis contract')
    return result


def win_interval(events, selected, baseline, samples=1000):
    ratios = events['sale_ratio'][:, study.HOLDS.index(1)]
    available = baseline & np.isfinite(ratios)
    if np.any(selected & ~baseline):
        raise ValueError('selected events must be a subset of the baseline')
    indices = np.flatnonzero(available)
    if not len(indices):
        return None
    dates, inverse = np.unique(events['date'][indices], return_inverse=True)
    if len(dates) < 20:
        return None
    chosen = selected[indices]
    wins = study.net_returns(ratios[indices], 0.002) > 0
    counts = np.bincount(inverse, minlength=len(dates))
    win_counts = np.bincount(inverse, weights=wins, minlength=len(dates))
    chosen_counts = np.bincount(inverse, weights=chosen, minlength=len(dates))
    chosen_wins = np.bincount(inverse, weights=chosen & wins, minlength=len(dates))
    if np.count_nonzero(chosen_counts) < 20:
        return None
    generator = np.random.default_rng(20261111)
    picks = generator.integers(0, len(dates), size=(samples, len(dates)))
    selected_denominator = chosen_counts[picks].sum(axis=1)
    valid = selected_denominator > 0
    if np.count_nonzero(valid) < samples * 0.99:
        return None
    chosen_rate = chosen_wins[picks[valid]].sum(axis=1) / selected_denominator[valid]
    baseline_rate = win_counts[picks[valid]].sum(axis=1) / counts[picks[valid]].sum(axis=1)
    return {
        'method': 'paired signal-day clustered bootstrap', 'samples': int(np.count_nonzero(valid)),
        'seed': 20261111, 'win_rate_95': np.quantile(chosen_rate, [0.025, 0.975]).tolist(),
        'win_difference_vs_baseline_95': np.quantile(chosen_rate - baseline_rate, [0.025, 0.975]).tolist(),
    }


def summarize_periods(events, selected, baseline, periods):
    result = {}
    for name, period in periods.items():
        current = selected & period
        result[name] = {
            '10bp': study.summarize(events, current, 1, 0.001),
            '20bp': study.summarize(events, current, 1, 0.002),
        }
        for cost in ('10bp', '20bp'):
            stats = result[name][cost]
            stats['not_wins'] = stats['closed'] - stats['wins']
            stats['break_even'] = stats['closed'] - stats['wins'] - stats['losses']
        if name in PRIMARY_PERIODS:
            result[name]['20bp_day_cluster_ci'] = study.bootstrap_pf(events, current, 1)
            result[name]['20bp_win_cluster_ci'] = win_interval(events, current, baseline & period)
    return result


def evaluate(events, features, cohorts):
    signals = {name: events[name] for name in ('date', 'prior_reference_valid', 'prior_limit', 'above_ma20', 'rsi6')}
    masks = filter_masks(signals, features)
    periods = analysis.period_masks(events['date'])
    records = []
    for cohort_name, cohort in cohorts.items():
        for filter_name, (accepted, known) in masks.items():
            selected = cohort & accepted
            results = summarize_periods(events, selected, cohort, periods)
            training = results['training_2021_2023']['20bp']
            selection = results['selection_2024']['20bp']
            sample_ok = study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)
            score = study.candidate_score(training, selection) if sample_ok else None
            targets = bool(sample_ok and score >= 1 and training['mean_return'] > 0 and selection['mean_return'] > 0)
            annual_ok = main_board.annual_and_uncertainty_gate(results)
            records.append({
                'cohort': cohort_name, 'filter': filter_name,
                'unknown_filter_events': int(np.count_nonzero(cohort & ~known)),
                'sample_ok': sample_ok,
                'min_development_win_rate': min(training['win_rate'], selection['win_rate']) if sample_ok else None,
                'profit_score': score, 'profit_targets_passed': targets,
                'annual_and_uncertainty_passed': annual_ok, 'strict_development_passed': targets and annual_ok,
                'periods': results,
                'excluded_periods': {name: study.summarize(events, cohort & known & ~accepted & period, 1, 0.002)
                                     for name, period in periods.items() if name in PRIMARY_PERIODS},
            })
    return records


def outcome_diagnostics(events, features, cohorts):
    periods = analysis.period_masks(events['date'])
    ratios = events['sale_ratio'][:, study.HOLDS.index(1)]
    resolved = np.isfinite(ratios)
    winning = resolved & (study.net_returns(ratios, 0.002) > 0)
    numeric = {'rsi6': events['rsi6'], 'gap': events['gap']}
    numeric.update({name: features[name] for name in NUMERIC_FEATURES if name in features})
    output = {}
    for cohort_name, cohort in cohorts.items():
        output[cohort_name] = {}
        for period_name in PRIMARY_PERIODS:
            base = cohort & periods[period_name]
            entries = {}
            for outcome_name, outcome in (('win', winning), ('not_win', resolved & ~winning)):
                subset = base & outcome
                medians = {}
                for feature_name, values in numeric.items():
                    selected = values[subset & np.isfinite(values)]
                    medians[feature_name] = float(np.median(selected)) if len(selected) else None
                entries[outcome_name] = {'count': int(np.count_nonzero(subset)), 'feature_medians': medians}
            entries['closed_at_limit_after_entry'] = study.summarize(events, base & events['closed_at_limit'], 1, 0.002)
            entries['not_closed_at_limit_after_entry'] = study.summarize(events, base & ~events['closed_at_limit'], 1, 0.002)
            output[cohort_name][period_name] = entries
    return output


def monthly_diagnostics(events, features, cohorts):
    signals = {name: events[name] for name in ('date', 'prior_reference_valid', 'prior_limit', 'above_ma20', 'rsi6')}
    masks = filter_masks(signals, features)
    selection = (events['date'] >= '2024-01-01') & (events['date'] < '2025-01-01')
    result = {}
    for cohort_name, cohort in cohorts.items():
        result[cohort_name] = {}
        for filter_name in ('none', 'prior_k_ge_50'):
            current = cohort & masks[filter_name][0] & selection
            result[cohort_name][filter_name] = {}
            for month in range(1, 13):
                prefix = f'2024-{month:02d}'
                monthly = np.char.startswith(events['date'], prefix)
                result[cohort_name][filter_name][prefix] = {
                    'month_only': study.summarize(events, current & monthly, 1, 0.002),
                    'other_months': study.summarize(events, current & ~monthly, 1, 0.002),
                }
    return result


def normal_board_open_ratio(view, entry_position, entry_price, board):
    if (board not in (10, 20) or entry_position < 0 or entry_position >= len(view['normal'])
            or not np.isfinite(entry_price) or entry_price <= 0):
        raise ValueError('invalid board-limit exit request')
    for position in range(entry_position + 1, len(view['normal'])):
        lower = study.limit_cents(view['previous_close'][position], -board)
        if view['normal'][position] and view['raw_open'][position] > lower:
            return float(view['adj_open'][position] / entry_price), position - entry_position - 1
    return np.nan, -1


def execution_sensitivity(events, features, cohorts, board_ratios, board_delays):
    if len(board_ratios) != len(events['date']) or len(board_delays) != len(events['date']):
        raise ValueError('invalid board-limit exit result shape')
    alternative = dict(events)
    alternative['sale_ratio'] = events['sale_ratio'].copy()
    alternative['delay'] = events['delay'].copy()
    column = study.HOLDS.index(1)
    alternative['sale_ratio'][:, column] = board_ratios
    alternative['delay'][:, column] = board_delays
    signals = {name: events[name] for name in ('date', 'prior_reference_valid', 'prior_limit', 'above_ma20', 'rsi6')}
    masks = filter_masks(signals, features)
    periods = analysis.period_masks(events['date'])
    output = {}
    for cohort_name, cohort in cohorts.items():
        output[cohort_name] = {}
        for filter_name in ('none', 'prior_k_ge_50'):
            output[cohort_name][filter_name] = {}
            for period_name in PRIMARY_PERIODS:
                selected = cohort & masks[filter_name][0] & periods[period_name]
                output[cohort_name][filter_name][period_name] = {
                    'five_percent_proxy': study.summarize(events, selected, 1, 0.002),
                    'normal_board_proxy': study.summarize(alternative, selected, 1, 0.002),
                    'different_exit_day': int(np.count_nonzero(selected & (events['delay'][:, column] != board_delays))),
                }
    return output


def parse_ths_table(record):
    data = record.get('business_data')
    if not isinstance(data, dict) or type(data.get('selectedSecuritiesCount')) is not int:
        raise ValueError('invalid THS selected-security count')
    text = data.get('answer')
    if not isinstance(text, str):
        raise ValueError('invalid THS answer')
    columns = []
    result = {}
    for line in text.splitlines():
        if not line.startswith('|'):
            continue
        cells = [cell.strip() for cell in line.strip().strip('|').split('|')]
        if cells[0] == '股票代码':
            columns = cells
            continue
        code = re.fullmatch(r'(\d{6})\.(SH|SZ)', cells[0])
        if code is None:
            continue
        if not columns or len(cells) != len(columns):
            raise ValueError('invalid THS answer table')
        symbol = code.group(2).lower() + code.group(1)
        if symbol in result:
            raise ValueError('duplicate THS security')
        matches = [index for index, column in enumerate(columns) if column.startswith('kdj(k值)')]
        if len(matches) > 1:
            raise ValueError('ambiguous THS KDJ columns')
        if record.get('name') in ('kdj_ge_50', 'kdj_lt_50') and not matches:
            raise ValueError('THS KDJ comparison lacks its indicator column')
        previous_date = record.get('previous_date')
        if matches and (not isinstance(previous_date, str)
                        or not columns[matches[0]].endswith(f'[{previous_date.replace("-", "")}]')):
            raise ValueError('THS KDJ indicator date differs from the previous trading day')
        try:
            value = float(cells[matches[0]]) if matches and cells[matches[0]] not in ('', '--') else None
        except ValueError as error:
            raise ValueError('invalid THS KDJ value') from error
        if value is not None and not np.isfinite(value):
            raise ValueError('invalid THS KDJ value')
        result[symbol] = value
    if len(result) != data['selectedSecuritiesCount']:
        raise ValueError('THS answer is incomplete or paginated')
    return result


def compare_ths_kdj(payload, events, features, candidates, calendar):
    if not isinstance(payload, dict) or not isinstance(payload.get('records'), list):
        raise ValueError('invalid THS evidence')
    calendar_positions = {str(date): index for index, date in enumerate(calendar)}
    if len(calendar_positions) != len(calendar):
        raise ValueError('duplicate THS comparison calendar dates')
    groups = {}
    for record in payload['records']:
        date = record.get('date')
        name = record.get('name')
        if not isinstance(date, str) or not ('2021-01-01' <= date < '2025-01-01'):
            raise ValueError('THS comparisons must use development dates')
        position = calendar_positions.get(date, 0)
        if position <= 0 or record.get('previous_date') != str(calendar[position - 1]):
            raise ValueError('THS comparison does not use the previous trading day')
        if name not in ('base', 'kdj_ge_50', 'kdj_lt_50') or name in groups.setdefault(date, {}):
            raise ValueError('unexpected or duplicate THS comparison')
        groups[date][name] = parse_ths_table(record)
    indices = {(str(code), str(date)): index for index, (code, date) in enumerate(zip(events['code'], events['date']))}
    if len(indices) != len(events['date']):
        raise ValueError('duplicate frozen development event')
    output = []
    for date, tables in sorted(groups.items()):
        if set(tables) != {'base', 'kdj_ge_50', 'kdj_lt_50'}:
            raise ValueError('THS comparison requires base and both KDJ partitions')
        baseline = set(tables['base'])
        greater = set(tables['kdj_ge_50'])
        less = set(tables['kdj_lt_50'])
        common = sorted(code for code in baseline if (code, date) in candidates and (code, date) in indices)
        mismatches = []
        differences = []
        distances = []
        unknown = 0
        for code in common:
            index = indices[(code, date)]
            local = features['kdj_k'][index]
            if not np.isfinite(local):
                unknown += 1
                continue
            if (code in greater) != (local >= 50) or (code in less) != (local < 50):
                mismatches.append(code)
            external = tables['kdj_ge_50'].get(code) if code in greater else tables['kdj_lt_50'].get(code)
            if external is not None:
                differences.append(abs(float(local) - external))
            distances.append(abs(float(local) - 50))
        output.append({
            'date': date, 'base_count': len(baseline), 'at_least_50_count': len(greater),
            'below_50_count': len(less), 'partitions_disjoint': not bool(greater & less),
            'partitions_cover_base': (greater | less) == baseline,
            'local_common_count': len(common), 'local_unknown_count': unknown,
            'classification_mismatches': mismatches, 'numeric_comparisons': len(differences),
            'median_absolute_difference': float(np.median(differences)) if differences else None,
            'maximum_absolute_difference': max(differences) if differences else None,
            'minimum_local_distance_to_50': min(distances) if distances else None,
        })
    if not output:
        raise ValueError('THS comparison is empty')
    return output


def verify_locked_baseline(records, reference):
    matches = [record for record in records if record['cohort'] == 'growth_non_st_3_to_7'
               and record['filter'] == 'none']
    references = [record for record in reference['closing_exit_results'] if record['policy'] == 'fixed_t1']
    if len(matches) != 1 or len(references) != 1:
        raise ValueError('missing locked fixed-T+1 baseline')
    for period in analysis.period_masks(np.array([], dtype='<U10')):
        for cost in ('10bp', '20bp'):
            actual = matches[0]['periods'][period][cost]
            expected = references[0]['periods'][period][cost]
            for field in ('signals', 'closed', 'unresolved', 'days', 'stocks', 'wins', 'losses',
                          'win_rate', 'mean_return', 'payoff', 'pf', 'delayed'):
                if actual[field] is None or expected[field] is None:
                    if actual[field] != expected[field]:
                        raise ValueError('fixed-T+1 baseline differs from locked ST analysis')
                elif not np.isclose(actual[field], expected[field], rtol=0, atol=1e-12):
                    raise ValueError('fixed-T+1 baseline differs from locked ST analysis')


def load_evidence(arguments, metadata):
    candidate_sha = study.digest_file(arguments.candidates)
    sample_sha = study.digest_file(arguments.sample)
    status_shas = [study.digest_file(path) for path in arguments.statuses]
    reference = json.loads(arguments.reference.read_text(encoding='utf-8'))
    if (reference.get('events_sha256') != metadata['events_sha256']
            or reference.get('database_sha256') != metadata['database_sha256']
            or reference.get('candidate_sha256') != candidate_sha
            or reference.get('sample_sha256') != sample_sha
            or reference.get('status_sha256') != status_shas
            or not reference.get('complete_candidate_coverage')
            or reference.get('holdout_signals_examined') is not False):
        raise ValueError('locked ST reference or provenance mismatch')
    candidate = json.loads(arguments.candidates.read_text(encoding='utf-8'))
    sample = json.loads(arguments.sample.read_text(encoding='utf-8'))
    if (candidate.get('schema_version') != 1 or candidate.get('events_sha256') != metadata['events_sha256']
            or sample.get('schema_version') != 1 or sample.get('events_sha256') != metadata['events_sha256']
            or sample.get('database_sha256') != metadata['database_sha256']):
        raise ValueError('candidate or fixed sample provenance mismatch')
    records = analysis.event_records(candidate['records'])
    expected_sample, available = minute_sample.sample_records(candidate['records'])
    if (sample.get('records') != expected_sample or sample.get('available_by_year') != available
            or len(records) != reference['candidate_count']):
        raise ValueError('candidate or fixed sample does not match locked reference')
    sample_keys = set(analysis.event_records(sample['records']))
    statuses, unknown = analysis.read_statuses(arguments.statuses, records, candidate_sha, sample_keys, sample_sha)
    if unknown or set(statuses) != set(records):
        raise ValueError('historical status coverage is incomplete')
    return records, statuses, reference, {'candidate_sha256': candidate_sha, 'sample_sha256': sample_sha,
                                          'status_sha256': status_shas,
                                          'reference_sha256': study.digest_file(arguments.reference)}


def replay_features(arguments, events, dates, records, statuses):
    date_index = {date: index for index, date in enumerate(dates)}
    cutoff = int(np.searchsorted(dates, '2025-01-01'))
    manifest = study.load_manifest(arguments.manifest)
    count = len(events['date'])
    features = {name: np.full(count, np.nan)
                for name in ('macd', 'kdj_k', 'dmi_difference', 'atr_ratio', 'volume_ratio')}
    features.update({name: np.zeros(count, dtype=bool) for name in ('close_known', 'close_up')})
    known_non_st = np.zeros(count, dtype=bool)
    board_ratios = np.full(count, np.nan)
    board_delays = np.full(count, -1, dtype=np.int16)
    seen = set()
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        codes = np.unique(events['code'])
        for number, code in enumerate(codes, start=1):
            code = str(code)
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest:
                raise ValueError('missing frozen adjusted-price manifest entry')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], dates[-1])
            view = exits.price_view(raw, adjusted)
            prior_indicators = indicators.calculate(adjusted[:cutoff], view['valid'][:cutoff])
            prior_volume = volume.calculate(adjusted[:cutoff], view['valid'][:cutoff])
            for index in np.flatnonzero(events['code'] == code):
                date = str(events['date'][index])
                position = date_index[date]
                if position >= cutoff:
                    raise ValueError('indicator replay must not use holdout signal dates')
                prior = analysis.features_before_signal(position, prior_indicators, prior_volume)
                for name in features:
                    features[name][index] = prior[name]
                upper = study.limit_cents(view['previous_close'][position], int(events['board'][index]))
                entry = upper * view['adj_open'][position] / view['raw_open'][position]
                actual = exits.fixed_open_ratio(view, position, entry, 1)
                expected = events['sale_ratio'][index, study.HOLDS.index(1)]
                if not np.isclose(actual, expected, rtol=0, atol=1e-10, equal_nan=True):
                    raise ValueError('fixed-T+1 exit differs from frozen event')
                board_ratios[index], board_delays[index] = normal_board_open_ratio(
                    view, position, entry, int(events['board'][index]))
                if events['board'][index] == 20:
                    key = (code, date)
                    candidate = np.isfinite(prior['dmi_difference']) and prior['dmi_difference'] > 0
                    if candidate != (key in records):
                        raise ValueError('growth candidates differ from locked PDI filter')
                    if candidate:
                        prices = raw[position, :4]
                        if np.any(prices % 10000 != 0) or (prices // 10000).tolist() != records[key]['daily_cents']:
                            raise ValueError('candidate daily prices differ from frozen input')
                        seen.add(key)
                        known_non_st[index] = not statuses[key]
            if number % 500 == 0 or number == len(codes):
                print(f'已核对 {number}/{len(codes)} 只开发期股票', flush=True)
    finally:
        connection.close()
    if seen != set(records):
        raise ValueError('candidate coverage differs from locked replay')
    return features, known_non_st, board_ratios, board_delays


def run(arguments):
    started = time.monotonic()
    if arguments.result.exists():
        raise ValueError('refusing to overwrite win-analysis result')
    metadata = json.loads((arguments.output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata.get('limited') or not metadata.get('database_sha256')):
        raise ValueError('analysis requires a full verified extraction')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (arguments.output / 'events.npz', metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    records, statuses, reference, evidence = load_evidence(arguments, metadata)
    all_events = study.load_events(arguments.output / 'events.npz')
    indices = development_indices(all_events)
    events = {name: values[indices] for name, values in all_events.items()}
    if not len(indices):
        raise ValueError('development events are unavailable')
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    if len(dates) != len(np.unique(dates)) or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('trading calendar must be unique and ordered')
    features, non_st, board_ratios, board_delays = replay_features(arguments, events, dates, records, statuses)
    cohorts = {
        'growth_non_st_3_to_7': (events['board'] == 20) & non_st,
        'main_st_unknown_0_to_3': (events['board'] == 10) & (events['gap'] >= 0) & (events['gap'] < 0.03),
        'main_st_unknown_3_to_7': (events['board'] == 10) & (events['gap'] >= 0.03) & (events['gap'] < 0.07),
    }
    results = evaluate(events, features, cohorts)
    verify_locked_baseline(results, reference)
    if [study.digest_file(path) for path in arguments.statuses] != evidence['status_sha256']:
        raise ValueError('status evidence changed during analysis')
    ths_comparison = None
    if arguments.ths_evidence is not None:
        ths_sha = study.digest_file(arguments.ths_evidence)
        payload = json.loads(arguments.ths_evidence.read_text(encoding='utf-8'))
        ths_comparison = compare_ths_kdj(payload, events, features, records, dates)
        if study.digest_file(arguments.ths_evidence) != ths_sha:
            raise ValueError('THS evidence changed during analysis')
        evidence['ths_evidence_sha256'] = ths_sha
    result = {
        'schema_version': 1, 'events_sha256': metadata['events_sha256'],
        'database_sha256': metadata['database_sha256'], 'evidence': evidence,
        'development_events_replayed': len(indices),
        'status_provider': 'BaoStock candidate isST, not independently verified; main-board status unknown',
        'assumed_fill': 'every non-open-limit intraday limit touch at its estimated limit price',
        'exit': 'first following trading-session open, delayed by the existing conservative proxy',
        'win': 'closed trade with net return strictly greater than zero',
        'costs': '10bp or 20bp per side plus 20bp round-trip fee',
        'holdout_signals_examined': False,
        'tested_combinations': len(results),
        'sample_eligible_combinations': sum(record['sample_ok'] for record in results),
        'profit_targets_passed_count': sum(record['profit_targets_passed'] for record in results),
        'strict_development_passed_count': sum(record['strict_development_passed'] for record in results),
        'ths_kdj_historical_comparison': ths_comparison,
        'outcome_diagnostics': outcome_diagnostics(events, features, cohorts),
        'posthoc_monthly_2024_diagnostics': monthly_diagnostics(events, features, cohorts),
        'execution_sensitivity_normal_board_proxy': execution_sensitivity(
            events, features, cohorts, board_ratios, board_delays),
        'results': results, 'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    with arguments.result.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')
    print(json.dumps({key: result[key] for key in ('development_events_replayed', 'tested_combinations',
                     'sample_eligible_combinations', 'profit_targets_passed_count',
                     'strict_development_passed_count')}, ensure_ascii=False), flush=True)


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
    parser.add_argument('--reference', type=pathlib.Path, required=True)
    parser.add_argument('--ths-evidence', type=pathlib.Path)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
