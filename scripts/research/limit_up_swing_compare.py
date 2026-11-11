#!/usr/bin/env python3
"""Replay the archived swing policy over the limit-up account's dates and universe."""

import argparse
import importlib
import json
import math
import pathlib
import sqlite3
import sys

import numpy as np

from limit_up_account_sharpe import write_result
import limit_up_study as study


SWING_ARCHIVE_SHA256 = '6a74055613d981335034fccd52ca35b34ac8cf30fe96c9b2f824dd7e1cca232f'


def matching_universe(stocks, active, missing):
    if len(stocks) != len(set(stocks)) or len(active) != len(set(active)):
        raise ValueError('duplicate stock in a frozen universe')
    growth = {code for code in active if study.limit_board(code) == 20}
    if not set(missing) <= growth or not growth <= set(stocks):
        raise ValueError('swing and limit-up universes do not align')
    available = growth - set(missing)
    return np.asarray([code in available for code in stocks], dtype=bool)


def entry_period(signal, first, last):
    if first < 1 or last < first:
        raise ValueError('invalid account period')
    entry = np.asarray(signal, dtype=np.int32) + 1
    return (entry >= first) & (entry <= last)


def frozen_paths(arguments, archive):
    paths = {
        'combined': arguments.ths_dir / 'combined-results-verified.json',
        'paths': arguments.ths_dir / 'expanded/paths.npz',
        'atr': arguments.ths_dir / 'atr-full/atr.npz',
        'closes': arguments.source_output / 'closes.npy',
        'calendar_metadata': arguments.source_output / 'study-meta.json',
        'hypotheses': arguments.risk_dir / 'hypotheses.json',
        'study': arguments.risk_dir / 'study.py',
        'account': arguments.risk_dir / 'account.py',
        'metrics': arguments.risk_dir / 'metrics.py',
        'exit_engine': arguments.prior_dir / 'engine.py',
        'sizing_engine': arguments.prior_dir / 'sized_portfolio.py',
        'optimizer': arguments.ths_dir / 'optimize.py',
    }
    if any(study.digest_file(path) != archive['sha256'].get(name) for name, path in paths.items()):
        raise ValueError('archived swing input or implementation hash mismatch')
    return paths


def verify_limit_assumptions(limit):
    expected = {'initial': 200000, 'position_cap': 20000, 'max_positions': 10,
                'annual_risk_free': 0.02, 'round_trip_fee_on_budget': 0.002}
    assumptions = limit.get('assumptions', {})
    if any(assumptions.get(name) != value for name, value in expected.items()):
        raise ValueError('matched comparison requires identical account and cost assumptions')


def verify_wave_alignment(limit, expanded, atr, archive, policies):
    inputs = expanded.get('input_hashes', {})
    if (expanded.get('limited') or expanded.get('policies') != policies
            or inputs.get('database') != limit['inputs']['database_sha256']
            or inputs.get('calendar') != limit['inputs']['calendar_sha256']
            or expanded.get('prices_manifest_sha256') != limit['inputs']['manifest_sha256']
            or atr.get('limited') or atr.get('source_paths_sha256') != archive['sha256']['paths']
            or atr.get('atr_data_sha256') != archive['sha256']['atr']):
        raise ValueError('swing input provenance or exit policy order does not align')


def run(arguments):
    if arguments.result.exists():
        raise ValueError('refusing to overwrite matched comparison')
    limit_sha = study.digest_file(arguments.limit_result)
    limit = json.loads(arguments.limit_result.read_text(encoding='utf-8'))
    if (limit.get('schema_version') != 1 or limit.get('holdout_signals_examined') is not False
            or study.digest_file(arguments.database) != limit['inputs']['database_sha256']
            or study.digest_file(arguments.calendar) != limit['inputs']['calendar_sha256']):
        raise ValueError('limit-up input provenance mismatch')
    verify_limit_assumptions(limit)
    available_scenarios = {(item['period'], item['annual_cash_yield'], item['slippage_per_side'])
                           for item in limit['scenarios'] if item['stage'] == 'first_touch'
                           and item['k50'] and item['exit'] == 't1'}
    expected_scenarios = {(period, cash, slip) for period in ('training_2021_2023', 'selection_2024')
                          for cash in (0.02, 0.0) for slip in (0.002, 0.003)}
    if not expected_scenarios <= available_scenarios:
        raise ValueError('limit-up account lacks matched cash or slippage scenarios')
    archive_path = arguments.risk_dir / 'result.json'
    archive_sha = study.digest_file(archive_path)
    if archive_sha != SWING_ARCHIVE_SHA256:
        raise ValueError('archived swing research hash mismatch')
    archive = json.loads(archive_path.read_text(encoding='utf-8'))
    paths = frozen_paths(arguments, archive)
    combined = json.loads(paths['combined'].read_text(encoding='utf-8'))
    parameters = combined['selected_variant']['chosen']
    expected = {'minimum_k': 70, 'trend': 'none', 'minimum_rsi': 50, 'dmi_mode': 'negative',
                'minimum_atr_fraction': 0.02, 'maximum_atr_fraction': 0.06,
                'ordering': 'rsi6_asc', 'policy': 'none/boll6/20'}
    if parameters != expected:
        raise ValueError('archived swing policy differs from SW-R010')
    metadata = json.loads(paths['calendar_metadata'].read_text(encoding='utf-8'))
    dates = np.asarray(metadata['calendar'])
    limit_dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                              if line.strip()])
    if not np.array_equal(dates, limit_dates):
        raise ValueError('swing and limit-up trading calendars differ')
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        active = [row[0] for row in connection.execute('SELECT code FROM stock WHERE active=1 ORDER BY code')]
    finally:
        connection.close()
    universe = matching_universe([row[0] for row in metadata['stocks']], active,
                                 limit['coverage']['missing_adjusted_stocks'])
    if int(np.count_nonzero(universe)) != limit['coverage']['scanned_growth_stocks']:
        raise ValueError('matched growth stock count differs from the limit-up scan')
    sys.path[:0] = [str(arguments.risk_dir), str(arguments.ths_dir), str(arguments.prior_dir)]
    account = importlib.import_module('account')
    metrics = importlib.import_module('metrics')
    engine = importlib.import_module('engine')
    optimize = importlib.import_module('optimize')
    expanded = importlib.import_module('expanded_policies')
    expanded_meta_path = arguments.ths_dir / 'expanded/meta.json'
    atr_meta_path = arguments.ths_dir / 'atr-full/meta.json'
    expanded_meta = json.loads(expanded_meta_path.read_text(encoding='utf-8'))
    atr_meta = json.loads(atr_meta_path.read_text(encoding='utf-8'))
    verify_wave_alignment(limit, expanded_meta, atr_meta, archive, [policy.name for policy in expanded.POLICIES])
    extra_paths = {name: pathlib.Path(module.__file__)
                   for name, module in (('expanded_policies', expanded),
                                        ('research_core', importlib.import_module('research_core')))}
    extra_paths.update({'expanded_metadata': expanded_meta_path, 'atr_metadata': atr_meta_path})
    extra_implementations = {name: study.digest_file(path) for name, path in extra_paths.items()}
    policy_index = next(index for index, policy in enumerate(expanded.POLICIES)
                        if policy.name == parameters['policy'])
    closes = np.load(paths['closes'], mmap_mode='r')
    if closes.shape != (len(universe), len(dates)):
        raise ValueError('swing valuation matrix does not align with the calendar and stock universe')
    with np.load(paths['paths'], allow_pickle=False) as data:
        inputs = {name: data[name] for name in data.files}
    if inputs['exit'].shape != (len(inputs['signal']), len(expanded.POLICIES)):
        raise ValueError('swing exit paths do not align with policy metadata')
    with np.load(paths['atr'], allow_pickle=False) as data:
        if not np.array_equal(data['source_index'], inputs['source_index']):
            raise ValueError('swing ATR rows do not align with frozen paths')
        inputs['atr_sma14_fraction'] = data['atr_sma14_fraction']
    scenarios = []
    for period, start, end in (('training_2021_2023', '2021-01-01', '2024-01-01'),
                               ('selection_2024', '2024-01-01', '2025-01-01')):
        first = int(np.searchsorted(dates, start))
        last = int(np.searchsorted(dates, end)) - 1
        relevant = entry_period(inputs['signal'], first, last) & universe[inputs['stock']]
        local = {name: values[relevant] for name, values in inputs.items()}
        selected = np.flatnonzero(optimize.params_mask(local, parameters))
        view = optimize.create_view(local, selected, policy_index, parameters['ordering'])
        view['allocation'] = np.full(len(selected), 20000.0)
        unresolved_candidates = int(np.count_nonzero((view['entry_status'] == 0) & (view['exit'] < 0)))
        for cash_yield in (0.02, 0.0):
            for slippage in (0.002, 0.003):
                portfolio, trades, daily = account.portfolio(
                    view, dates, closes, first, last, initial=200000., cap=20000., limit=10,
                    slippage=slippage, fee=0.002, annual_cash_yield=cash_yield)
                statistics = engine.stats(trades, dates, first, last)
                risk = metrics.summarize(daily, 200000., 0.02)
                if not portfolio['open_positions']:
                    reconciled = 200000. + sum(trade['pnl'] for trade in trades) + portfolio['cash_interest']
                    if not math.isclose(portfolio['final_equity'], reconciled, rel_tol=1e-10, abs_tol=1e-7):
                        raise ValueError('swing account equity does not reconcile')
                incomplete = portfolio['counters']['stale_position_days'] > 0 or unresolved_candidates > 0
                risk['diagnostic_sharpe_before_data_gate'] = risk['sharpe']
                risk['data_complete'] = not incomplete
                if incomplete:
                    risk['sharpe'] = None
                fields = ('n', 'stocks', 'days', 'wins', 'losses', 'win_rate', 'pf', 'average_win',
                          'average_loss', 'reward_risk_cash', 'mean_return')
                scenarios.append({
                    'period': period, 'annual_cash_yield': cash_yield, 'slippage_per_side': slippage,
                    'candidate_signals': len(selected), 'unresolved_candidate_paths': unresolved_candidates,
                    'account': portfolio, 'trade_metrics': {name: statistics[name] for name in fields},
                    'risk_metrics': risk,
                    'daily': [{'date': str(dates[day]), 'equity': equity, 'cash': cash, 'positions': count}
                              for day, equity, cash, count in daily],
                    'trades': [{**trade, 'code': metadata['stocks'][trade['stock']][0],
                                'entry_date': str(dates[trade['entry']]), 'exit_date': str(dates[trade['exit']])}
                               for trade in trades],
                })
    frozen_paths(arguments, archive)
    if study.digest_file(arguments.limit_result) != limit_sha or study.digest_file(archive_path) != archive_sha:
        raise ValueError('comparison reference changed during analysis')
    if any(study.digest_file(path) != extra_implementations[name] for name, path in extra_paths.items()):
        raise ValueError('comparison implementation or metadata changed during analysis')
    result = {
        'schema_version': 1, 'limit_result_sha256': limit_sha, 'swing_archive_sha256': archive_sha,
        'swing_inputs_sha256': archive['sha256'], 'extra_implementation_sha256': extra_implementations,
        'universe': 'same available 20% growth-board stock codes as the limit-up account',
        'stock_count': int(np.count_nonzero(universe)), 'holdout_signals_examined': False,
        'entry_window': 'each account resets on first session; prior-session signal may enter on the first day',
        'round_trip_fee_on_budget': 0.002, 'annual_risk_free': 0.02, 'scenarios': scenarios,
    }
    write_result(arguments.result, result)
    print(json.dumps({'matched_stocks': result['stock_count'], 'scenarios': len(scenarios)}, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--risk-dir', type=pathlib.Path, required=True)
    parser.add_argument('--ths-dir', type=pathlib.Path, required=True)
    parser.add_argument('--prior-dir', type=pathlib.Path, required=True)
    parser.add_argument('--source-output', type=pathlib.Path, required=True)
    parser.add_argument('--database', type=pathlib.Path, required=True)
    parser.add_argument('--calendar', type=pathlib.Path, required=True)
    parser.add_argument('--limit-result', type=pathlib.Path, required=True)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
