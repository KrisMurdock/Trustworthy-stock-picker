#!/usr/bin/env python3
"""Audit four limit-up branches and their historical indicator sensitivities."""

import argparse
import json
import pathlib
import time

import numpy as np

import limit_up_account_sharpe as account
import limit_up_momentum_boll as momentum
import limit_up_study as study
import limit_up_win_analysis as win


PRIMARY = ('training_2021_2023', 'selection_2024')


def sample_deviation_features(features):
    result = dict(features)
    middle = np.asarray(features['middle'], dtype=float)
    upper = np.asarray(features['upper'], dtype=float)
    lower = np.asarray(features['lower'], dtype=float)
    if middle.ndim != 1 or upper.shape != middle.shape or lower.shape != middle.shape:
        raise ValueError('inconsistent Bollinger features')
    factor = np.sqrt(20 / 19)
    result['upper'] = middle + (upper - middle) * factor
    result['lower'] = middle + (lower - middle) * factor
    return result


def verify_reference(reference, metadata, evidence, reproduced):
    names = ('events_sha256', 'database_sha256', 'calendar_sha256', 'manifest_sha256')
    if (reference.get('schema_version') != 1 or reference.get('holdout_signals_examined') is not False
            or any(reference.get(name) != metadata.get(name) for name in names)
            or reference.get('evidence') != evidence or reference.get('results') != reproduced):
        raise ValueError('momentum reference or reproduced results differ')


def loss_amount(events, mask):
    ratios = events['sale_ratio'][mask, study.HOLDS.index(1)]
    returns = study.net_returns(ratios[np.isfinite(ratios)], 0.002)
    return float(-returns[returns < 0].sum())


def failure_breakdown(events, selected):
    selected = np.asarray(selected, dtype=bool)
    if selected.shape != events['date'].shape:
        raise ValueError('invalid diagnostic selection')
    column = study.HOLDS.index(1)
    ratios = events['sale_ratio'][:, column]
    delays = events['delay'][:, column]
    resolved = np.isfinite(ratios)
    if np.any(selected & ((resolved & (delays < 0)) | (~resolved & (delays >= 0)))):
        raise ValueError('inconsistent exit availability')
    delayed = resolved & (delays > 0)
    undelayed = resolved & (delays == 0)
    groups = {
        'sealed_at_buy_day_close': selected & events['closed_at_limit'],
        'not_sealed_at_buy_day_close': selected & ~events['closed_at_limit'],
        'next_open_below_entry_undelayed': selected & undelayed & (ratios < 1),
        'next_open_at_or_above_entry_undelayed': selected & undelayed & (ratios >= 1),
        'delayed_or_missing_data_proxy': selected & delayed,
        'unresolved': selected & ~resolved,
    }
    total_loss = loss_amount(events, selected)
    result = {}
    for name, mask in groups.items():
        loss = loss_amount(events, mask)
        result[name] = {'metrics': study.summarize(events, mask, 1, 0.002),
                        'loss_amount': loss,
                        'share_of_total_loss': loss / total_loss if total_loss else None}
    return result


def eligible_pair(training, selection):
    return study.eligible(training, 300, 80, 50) and study.eligible(selection, 80, 40, 25)


def grid_summary(results):
    output = {}
    for branch in momentum.BRANCHES:
        eligible = []
        passed = []
        for item in results:
            if item['branch'] != branch:
                continue
            training = item['groups'][PRIMARY[0] + '/all']['all_statuses_diagnostic']
            selection = item['groups'][PRIMARY[1] + '/all']['all_statuses_diagnostic']
            if not eligible_pair(training, selection):
                continue
            score = study.candidate_score(training, selection)
            summary = {'alternate_rsi': item['alternate_rsi'], 'enhancement': item['enhancement'],
                       'exit': item['exit'], 'score': score,
                       'training': training, 'selection_2024': selection}
            eligible.append(summary)
            if (training['mean_return'] > 0 and selection['mean_return'] > 0
                    and training['pf'] >= 1.5 and selection['pf'] >= 1.5
                    and training['payoff'] >= 1.2 and selection['payoff'] >= 1.2):
                passed.append(summary)
        eligible.sort(key=lambda value: value['score'], reverse=True)
        output[branch] = {'configs': sum(item['branch'] == branch for item in results),
                          'eligible': len(eligible), 'passed_development_targets': len(passed),
                          'closest_in_sample': eligible[0] if eligible else None}
    return output


def branch_details(events, features, statuses):
    non_st, st, unknown = momentum.status_masks(events, statuses)
    branches = momentum.branch_masks(features)
    output = {}
    for branch, branch_mask in branches.items():
        periods = {}
        for name, (start, end) in momentum.PERIODS.items():
            selected = branch_mask & (events['date'] >= start) & (events['date'] < end)
            interval = win.win_interval(events, selected, selected)
            periods[name] = {
                '10bp': study.summarize(events, selected, 1, 0.001),
                '20bp': study.summarize(events, selected, 1, 0.002),
                '20bp_day_cluster_pf_mean_ci': study.bootstrap_pf(events, selected, 1),
                '20bp_day_cluster_win_ci': interval['win_rate_95'] if interval else None,
                'historical_status': {'known_non_st': int(np.count_nonzero(selected & non_st)),
                                      'known_st': int(np.count_nonzero(selected & st)),
                                      'unknown': int(np.count_nonzero(selected & unknown))},
                'post_entry_outcomes': failure_breakdown(events, selected),
            }
        output[branch] = periods
    return output


def changed_memberships(original, sample):
    left = momentum.branch_masks(original)
    right = momentum.branch_masks(sample)
    return {name: {'added': int(np.count_nonzero(right[name] & ~left[name])),
                   'removed': int(np.count_nonzero(left[name] & ~right[name]))}
            for name in momentum.BRANCHES}


def run(arguments):
    started = time.monotonic()
    if arguments.result.exists():
        raise ValueError('refusing to overwrite diagnostic result')
    metadata, _, statuses, evidence = momentum.check_inputs(arguments)
    baseline_sha = study.digest_file(arguments.baseline)
    reference = json.loads(arguments.baseline.read_text(encoding='utf-8'))
    all_events = study.load_events(arguments.input / 'events.npz')
    development = (all_events['date'] >= momentum.FIRST_DATE) & (all_events['date'] < momentum.END_DATE)
    events = {name: values[development] for name, values in all_events.items()}
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if len(dates) < 100 or np.any(dates[1:] <= dates[:-1]):
        raise ValueError('invalid frozen trading calendar')
    manifest = study.load_manifest(arguments.manifest)
    features = momentum.extract_features(arguments, events, dates, manifest)
    original_results = momentum.evaluate(events, features, statuses)
    verify_reference(reference, metadata, evidence, original_results)
    sample_features = sample_deviation_features(features)
    sample_results = momentum.evaluate(events, sample_features, statuses)
    result = {
        'schema_version': 1,
        'input_hashes': {name: metadata[name] for name in ('events_sha256', 'database_sha256',
                                                          'calendar_sha256', 'manifest_sha256')},
        'evidence': evidence, 'baseline_sha256': baseline_sha,
        'development_events': len(events['date']),
        'signal_first': momentum.FIRST_DATE, 'signal_before': momentum.END_DATE,
        'holdout_signals_examined': False, 'account_metrics_available': False,
        'warning': ('idealized equal-weight event diagnostics only; mostly unknown historical ST, '
                    'no verified queue fills, no valid account Sharpe or independent holdout'),
        'confidence_intervals': '1000 signal-day clustered bootstrap samples; no multiple-testing correction',
        'sample_deviation': '20-session sample standard deviation, ddof=1; provisional THS parity only',
        'post_entry_groups': 'descriptive outcomes, not pre-entry filters or executable fill evidence',
        'baseline_grid': grid_summary(original_results),
        'sample_deviation_grid': grid_summary(sample_results),
        'sample_deviation_results': sample_results,
        'sample_membership_changes': changed_memberships(features, sample_features),
        'baseline_details': branch_details(events, features, statuses),
        'sample_deviation_details': branch_details(events, sample_features, statuses),
        'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    if study.digest_file(arguments.baseline) != baseline_sha:
        raise ValueError('baseline changed during diagnostics')
    account.write_result(arguments.result, result)
    print(json.dumps({name: result[name] for name in ('schema_version', 'development_events',
                     'holdout_signals_examined', 'sample_membership_changes', 'elapsed_seconds')},
                     ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('database', 'calendar', 'qfq-dir', 'manifest', 'input', 'candidates', 'sample',
                 'reference', 'baseline', 'result'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    parser.add_argument('--statuses', type=pathlib.Path, nargs='+', required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
