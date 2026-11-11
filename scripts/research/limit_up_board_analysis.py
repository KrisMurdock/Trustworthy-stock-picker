#!/usr/bin/env python3
"""Describe first-board and continuation-touch events without changing entry rules."""

import argparse
import json
import pathlib
import sqlite3
import time

import numpy as np

import limit_up_exits as exits
import limit_up_minutes as minute_sample
import limit_up_st_analysis as analysis
import limit_up_study as study
import limit_up_win_analysis as win_analysis


PRIMARY_PERIODS = ('training_2021_2023', 'selection_2024')
GROUPS = ('first_touch', 'continuation_touch', 'one_to_two', 'two_or_more',
          'continuation_depth_unknown', 'prior_unknown')


def board_states(raw, adjusted, percent, view=None):
    if percent not in (10, 20):
        raise ValueError('unsupported normal-board price limit')
    if view is None:
        view = exits.price_view(raw, adjusted)
    previous_valid = np.r_[False, view['valid'][:-1]]
    high = (raw[:, 1] + 5000) // 10000
    close = (raw[:, 3] + 5000) // 10000
    upper = study.limit_cents(view['previous_close'], percent)
    known = view['normal'] & previous_valid & (high <= upper)
    sealed = known & (close == upper)
    return known, sealed


def hold_until_break_exit(view, known, sealed, position, percent, max_hold=5, sell_limit=5):
    known = np.asarray(known, dtype=bool)
    sealed = np.asarray(sealed, dtype=bool)
    size = len(view['valid'])
    if (known.shape != (size,) or sealed.shape != (size,) or np.any(sealed & ~known)
            or percent not in (10, 20) or sell_limit not in (5, percent)
            or not 0 <= position < size or max_hold < 1):
        raise ValueError('invalid holding-path inputs')
    if not known[position] or view['raw_open'][position] <= 0:
        raise ValueError('entry-day board reference is unknown')
    entry_limit = study.limit_cents(view['previous_close'][position], percent)
    entry_price = entry_limit * view['adj_open'][position] / view['raw_open'][position]
    for offset in range(max_hold):
        current = position + offset
        if current >= size or not known[current]:
            return np.nan, -1, -1, 'board_unknown'
        if not sealed[current] or offset == max_hold - 1:
            reason = 'break_close' if not sealed[current] else 'max_hold'
            target = current + 1
            for exit_position in range(target, size):
                lower = study.limit_cents(view['previous_close'][exit_position], -sell_limit)
                if view['normal'][exit_position] and view['raw_open'][exit_position] > lower:
                    return (view['adj_open'][exit_position] / entry_price,
                            exit_position - target, offset + 1, reason)
            return np.nan, -1, offset + 1, 'exit_unavailable'
    raise ValueError('holding path has no termination')


def board_groups(previous_known, previous_sealed, earlier_known, earlier_sealed):
    arrays = [np.asarray(value, dtype=bool) for value in
              (previous_known, previous_sealed, earlier_known, earlier_sealed)]
    if any(value.ndim != 1 or value.shape != arrays[0].shape for value in arrays):
        raise ValueError('inconsistent prior-board arrays')
    previous_known, previous_sealed, earlier_known, earlier_sealed = arrays
    if np.any(previous_sealed & ~previous_known) or np.any(earlier_sealed & ~earlier_known):
        raise ValueError('unknown board state cannot be marked sealed')
    continuation = previous_known & previous_sealed
    groups = {
        'first_touch': previous_known & ~previous_sealed,
        'continuation_touch': continuation,
        'one_to_two': continuation & earlier_known & ~earlier_sealed,
        'two_or_more': continuation & earlier_known & earlier_sealed,
        'continuation_depth_unknown': continuation & ~earlier_known,
        'prior_unknown': ~previous_known,
    }
    return groups


def board_outcome(events, selected, next_known, next_sealed):
    count = len(events['date'])
    arrays = [np.asarray(value, dtype=bool) for value in (selected, next_known, next_sealed)]
    if any(value.shape != (count,) for value in arrays) or np.any(arrays[2] & ~arrays[1]):
        raise ValueError('invalid next-day board outcomes')
    selected, next_known, next_sealed = arrays
    sealed_today = selected & events['closed_at_limit']
    observed = sealed_today & next_known
    continued = observed & next_sealed
    unresolved = sealed_today & ~next_known
    denominator = int(np.count_nonzero(selected) - np.count_nonzero(unresolved))
    observed_count = int(np.count_nonzero(observed))
    continued_count = int(np.count_nonzero(continued))
    return {
        'touches': int(np.count_nonzero(selected)),
        'sealed_buy_day': int(np.count_nonzero(sealed_today)),
        'next_day_known_after_seal': observed_count,
        'next_day_unknown_after_seal': int(np.count_nonzero(unresolved)),
        'sealed_both_days': continued_count,
        'next_seal_rate_given_buy_day_seal': continued_count / observed_count if observed_count else None,
        'both_days_sealed_rate_known': continued_count / denominator if denominator else None,
    }


def replay_paths(arguments, events, dates):
    if np.any(events['date'] < '2021-01-01') or np.any(events['date'] >= '2025-01-01'):
        raise ValueError('board replay is restricted to development signal dates')
    date_index = {str(date): index for index, date in enumerate(dates)}
    manifest = study.load_manifest(arguments.manifest)
    paths = {name: np.zeros(len(events['date']), dtype=bool) for name in
             ('previous_known', 'previous_sealed', 'earlier_known', 'earlier_sealed',
              'next_known', 'next_sealed')}
    paths['holding_ratio'] = np.full(len(events['date']), np.nan)
    paths['holding_delay'] = np.full(len(events['date']), -1, dtype=np.int16)
    paths['holding_target'] = np.full(len(events['date']), -1, dtype=np.int16)
    paths['holding_reason'] = np.full(len(events['date']), '', dtype='<U24')
    paths['holding_board_ratio'] = np.full(len(events['date']), np.nan)
    paths['holding_board_delay'] = np.full(len(events['date']), -1, dtype=np.int16)
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        codes = np.unique(events['code'])
        for number, code_value in enumerate(codes, start=1):
            code = str(code_value)
            path = arguments.qfq_dir / (code + '.json.gz')
            if path.name not in manifest:
                raise ValueError('missing frozen adjusted-price manifest entry')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], dates[-1])
            view = exits.price_view(raw, adjusted)
            known, sealed = board_states(raw, adjusted, study.limit_board(code), view)
            for index in np.flatnonzero(events['code'] == code):
                position = date_index[str(events['date'][index])]
                if position < 2 or position + 1 >= len(dates):
                    raise ValueError('board path lacks adjacent calendar sessions')
                if not known[position] or bool(sealed[position]) != bool(events['closed_at_limit'][index]):
                    raise ValueError('signal-day board differs from frozen event')
                if known[position - 1] and (
                        not events['prior_reference_valid'][index]
                        or bool(sealed[position - 1]) != bool(events['prior_limit'][index])):
                    raise ValueError('previous board differs from frozen event')
                paths['previous_known'][index] = known[position - 1]
                paths['previous_sealed'][index] = sealed[position - 1]
                paths['earlier_known'][index] = known[position - 2]
                paths['earlier_sealed'][index] = sealed[position - 2]
                paths['next_known'][index] = known[position + 1]
                paths['next_sealed'][index] = sealed[position + 1]
                ratio, delay, target, reason = hold_until_break_exit(
                    view, known, sealed, position, int(events['board'][index]))
                paths['holding_ratio'][index] = ratio
                paths['holding_delay'][index] = delay
                paths['holding_target'][index] = target
                paths['holding_reason'][index] = reason
                board_ratio, board_delay, board_target, board_reason = hold_until_break_exit(
                    view, known, sealed, position, int(events['board'][index]),
                    sell_limit=int(events['board'][index]))
                if board_target != target or (board_reason != reason
                                              and 'exit_unavailable' not in (board_reason, reason)):
                    raise ValueError('exit proxy changed the pre-exit holding decision')
                paths['holding_board_ratio'][index] = board_ratio
                paths['holding_board_delay'][index] = board_delay
            if number % 500 == 0 or number == len(codes):
                print(f'已核对 {number}/{len(codes)} 只股票的前后封板状态', flush=True)
    finally:
        connection.close()
    return paths


def verify_baseline(events, selected, expected):
    for period_name, period in analysis.period_masks(events['date']).items():
        for cost_name, slippage in (('10bp', 0.001), ('20bp', 0.002)):
            actual = study.summarize(events, selected & period, 1, slippage)
            reference = expected['periods'][period_name][cost_name]
            for field in ('signals', 'closed', 'unresolved', 'days', 'stocks', 'wins', 'losses',
                          'win_rate', 'mean_return', 'payoff', 'pf', 'delayed'):
                if actual[field] is None or reference[field] is None:
                    if actual[field] != reference[field]:
                        raise ValueError('cohort differs from locked baseline')
                elif not np.isclose(actual[field], reference[field], rtol=0, atol=1e-12):
                    raise ValueError('cohort differs from locked baseline')


def summarize_cohort(events, selected, paths, alternate_events=None):
    groups = board_groups(paths['previous_known'], paths['previous_sealed'],
                          paths['earlier_known'], paths['earlier_sealed'])
    periods = analysis.period_masks(events['date'])
    holding_events = {name: values.copy() if name in ('sale_ratio', 'delay') else values
                      for name, values in events.items()}
    holding_events['sale_ratio'][:, study.HOLDS.index(1)] = paths['holding_ratio']
    holding_events['delay'][:, study.HOLDS.index(1)] = paths['holding_delay']
    holding_board_events = {name: values.copy() if name in ('sale_ratio', 'delay') else values
                            for name, values in events.items()}
    holding_board_events['sale_ratio'][:, study.HOLDS.index(1)] = paths['holding_board_ratio']
    holding_board_events['delay'][:, study.HOLDS.index(1)] = paths['holding_board_delay']
    output = {}
    for group_name in GROUPS:
        result = {}
        for period_name, period in periods.items():
            mask = selected & groups[group_name] & period
            metrics = {}
            for hold in (1, 3):
                current = {
                    '10bp': study.summarize(events, mask, hold, 0.001),
                    '20bp': study.summarize(events, mask, hold, 0.002),
                }
                if period_name in PRIMARY_PERIODS and group_name in ('first_touch', 'continuation_touch'):
                    current['20bp_day_cluster_ci'] = study.bootstrap_pf(events, mask, hold)
                    if hold == 1:
                        current['20bp_win_cluster_ci'] = win_analysis.win_interval(events, mask, mask)
                if alternate_events is not None and hold == 1:
                    current['20bp_normal_board_exit_proxy'] = study.summarize(alternate_events, mask, hold, 0.002)
                metrics[f't_plus_{hold}'] = current
            metrics['post_entry_board_outcome'] = board_outcome(
                events, mask, paths['next_known'], paths['next_sealed'])
            metrics['hold_until_break_max_5'] = {
                '10bp': study.summarize(holding_events, mask, 1, 0.001),
                '20bp': study.summarize(holding_events, mask, 1, 0.002),
                '20bp_normal_board_exit_proxy': study.summarize(holding_board_events, mask, 1, 0.002),
                'reasons': {reason: int(np.count_nonzero(mask & (paths['holding_reason'] == reason)))
                            for reason in ('break_close', 'max_hold', 'board_unknown', 'exit_unavailable')},
            }
            if period_name in PRIMARY_PERIODS and group_name in ('first_touch', 'continuation_touch'):
                metrics['hold_until_break_max_5']['20bp_day_cluster_ci'] = study.bootstrap_pf(
                    holding_events, mask, 1)
            result[period_name] = metrics
        training = result['training_2021_2023']['t_plus_1']['20bp']
        selection = result['selection_2024']['t_plus_1']['20bp']
        sample_ok = study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)
        targets = bool(sample_ok and study.candidate_score(training, selection) >= 1
                       and training['mean_return'] > 0 and selection['mean_return'] > 0)
        mask_2024 = selected & groups[group_name] & periods['selection_2024']
        september = np.char.startswith(events['date'], '2024-09')
        output[group_name] = {
            'sample_ok': sample_ok, 'profit_targets_passed': targets, 'periods': result,
            'posthoc_september_2024': {
                'september': study.summarize(events, mask_2024 & september, 1, 0.002),
                'other_months': study.summarize(events, mask_2024 & ~september, 1, 0.002),
            },
        }
    return output


def run(arguments):
    started = time.monotonic()
    if arguments.result.exists():
        raise ValueError('refusing to overwrite board-analysis result')
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
    records, statuses, reference, evidence = win_analysis.load_evidence(arguments, metadata)
    win_reference_sha = study.digest_file(arguments.win_reference)
    win_reference = json.loads(arguments.win_reference.read_text(encoding='utf-8'))
    if (win_reference.get('events_sha256') != metadata['events_sha256']
            or win_reference.get('database_sha256') != metadata['database_sha256']
            or win_reference.get('holdout_signals_examined') is not False):
        raise ValueError('locked win reference provenance mismatch')
    all_events = study.load_events(arguments.output / 'events.npz')
    indices = win_analysis.development_indices(all_events)
    events = {name: values[indices] for name, values in all_events.items()}
    if not len(indices):
        raise ValueError('development events are unavailable')
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if len(dates) != len(np.unique(dates)) or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('trading calendar must be unique and ordered')
    paths = replay_paths(arguments, events, dates)
    growth_indices = np.flatnonzero(study.select_mask(events, minute_sample.BASE_CONFIG))
    growth_events = {name: values[growth_indices] for name, values in events.items()}
    features, non_st, board_ratios, board_delays = win_analysis.replay_features(
        arguments, growth_events, dates, records, statuses)
    growth_paths = {name: values[growth_indices] for name, values in paths.items()}
    alternate = {name: values.copy() for name, values in growth_events.items()}
    alternate['sale_ratio'][:, study.HOLDS.index(1)] = board_ratios
    alternate['delay'][:, study.HOLDS.index(1)] = board_delays
    base_reference = [record for record in reference['closing_exit_results'] if record['policy'] == 'fixed_t1']
    k_reference = [record for record in win_reference['results']
                   if record['cohort'] == 'growth_non_st_3_to_7' and record['filter'] == 'prior_k_ge_50']
    if len(base_reference) != 1 or len(k_reference) != 1:
        raise ValueError('locked baseline reference is missing or ambiguous')
    k_cohort = non_st & np.isfinite(features['kdj_k']) & (features['kdj_k'] >= 50)
    verify_baseline(growth_events, non_st, base_reference[0])
    verify_baseline(growth_events, k_cohort, k_reference[0])
    cohorts = {
        'growth_non_st_base': summarize_cohort(growth_events, non_st, growth_paths, alternate),
        'growth_non_st_k_ge_50': summarize_cohort(growth_events, k_cohort, growth_paths, alternate),
        'main_st_unknown_0_to_3': summarize_cohort(
            events, (events['board'] == 10) & (events['gap'] >= 0) & (events['gap'] < 0.03), paths),
        'main_st_unknown_3_to_7': summarize_cohort(
            events, (events['board'] == 10) & (events['gap'] >= 0.03) & (events['gap'] < 0.07), paths),
    }
    if [study.digest_file(path) for path in arguments.statuses] != evidence['status_sha256']:
        raise ValueError('status evidence changed during analysis')
    if study.digest_file(arguments.win_reference) != win_reference_sha:
        raise ValueError('win reference changed during analysis')
    result = {
        'schema_version': 1, 'events_sha256': metadata['events_sha256'],
        'database_sha256': metadata['database_sha256'], 'evidence': evidence,
        'win_reference_sha256': win_reference_sha, 'development_events_replayed': len(indices),
        'holdout_signals_examined': False,
        'status_scope': 'candidate-day BaoStock status for growth only; previous and following status unknown',
        'board_classification': 'normal 10/20 percent close proxy; invalid prior data remains unknown',
        'entry': 'pre-existing daily-bar touch event; same-day closing state is not used for entry',
        'exit': 'fixed T+1/T+3 or next open after first unsealed close, capped at T+5; '
                '5-percent and normal-board exit proxies compared; unknown board path remains unresolved',
        'costs': '10bp or 20bp per side plus 20bp round-trip fee',
        'limitations': 'development-only, post-hoc subgroup analysis; touch fill and real order queue unknown',
        'cohorts': cohorts, 'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    with arguments.result.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')
    print(json.dumps({'development_events_replayed': len(indices), 'cohorts': list(cohorts),
                      'elapsed_seconds': result['elapsed_seconds']}, ensure_ascii=False), flush=True)


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
    parser.add_argument('--win-reference', type=pathlib.Path, required=True)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
