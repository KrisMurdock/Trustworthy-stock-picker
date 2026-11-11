#!/usr/bin/env python3
"""Replay bounded limit-up accounts and calculate daily-equity Sharpe ratios."""

import argparse
import math
from collections import defaultdict
from dataclasses import dataclass
import json
import os
import pathlib
import sqlite3
import tempfile
import time

import numpy as np

import limit_up_board_analysis as board_analysis
import limit_up_exits as exits
import limit_up_indicators as indicators
import limit_up_minutes as minute_sample
import limit_up_st_analysis as analysis
import limit_up_study as study
import limit_up_win_analysis as win_analysis


@dataclass(frozen=True)
class Watch:
    code: str
    rsi: float
    stage: str
    k50: bool


@dataclass(frozen=True)
class Entry:
    code: str
    day: int
    is_st: bool
    price: float
    fixed_exit: int | None
    fixed_sale: float | None
    holding_exit: int | None
    holding_sale: float | None


def write_result(path, result):
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=path.parent,
                                         prefix='.' + path.name + '.', suffix='.tmp', delete=False) as stream:
            temporary = pathlib.Path(stream.name)
            json.dump(result, stream, ensure_ascii=False, indent=2, allow_nan=False)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def account_metrics(daily, initial, annual_risk_free):
    if (not daily or not math.isfinite(initial) or initial <= 0
            or not math.isfinite(annual_risk_free) or annual_risk_free < 0):
        raise ValueError('invalid account or risk-free assumptions')
    equity = np.asarray([row['equity'] for row in daily], dtype=float)
    cash = np.asarray([row['cash'] for row in daily], dtype=float)
    positions = np.asarray([row['positions'] for row in daily], dtype=int)
    if np.any(~np.isfinite(equity) | (equity <= 0) | ~np.isfinite(cash) | (cash < -1e-7)):
        raise ValueError('account equity and cash must be finite and valid')
    returns = equity / np.r_[initial, equity[:-1]] - 1
    rate = math.expm1(math.log1p(annual_risk_free) / 252)
    volatility = float(np.std(returns, ddof=1)) if len(returns) > 1 else 0.0
    sharpe = float((np.mean(returns) - rate) / volatility * math.sqrt(252)) if volatility > 1e-12 else None
    peak = np.maximum.accumulate(np.r_[initial, equity])[1:]
    return {
        'annual_mean_return': float(np.mean(returns) * 252),
        'annual_volatility': volatility * math.sqrt(252),
        'sharpe': sharpe,
        'cumulative_return': float(equity[-1] / initial - 1),
        'max_drawdown': float(-np.min(equity / peak - 1)),
        'mean_eod_cash_fraction': float(np.mean(cash / equity)),
        'flat_day_fraction': float(np.mean(positions == 0)),
        'sessions': len(daily),
    }


def premarket_watches(code, dates, raw, adjusted, view=None, calculated=None):
    if study.limit_board(code) != 20 or len(raw) != len(dates) or len(adjusted) != len(dates):
        raise ValueError('unsupported stock or inconsistent price shapes')
    cutoff = int(np.searchsorted(dates, '2025-01-01'))
    if cutoff < 62:
        raise ValueError('insufficient development calendar')
    if view is None:
        view = exits.price_view(raw, adjusted)
    known, sealed = board_analysis.board_states(raw, adjusted, 20, view)
    valid = view['valid'][:cutoff]
    prior_valid = np.r_[False, valid[:-1]]
    recent = study.rolling_mean(np.ones(cutoff), valid, 60)
    complete_previous = np.r_[False, np.isfinite(recent[:-1])]
    close = adjusted[:cutoff, 3]
    prior_close = np.r_[np.nan, close[:-1]]
    ma20 = study.rolling_mean(close, valid, 20)
    ma60 = study.rolling_mean(close, valid, 60)
    prior_ma20 = np.r_[np.nan, ma20[:-1]]
    prior_ma60 = np.r_[np.nan, ma60[:-1]]
    previous_raw_close = np.r_[0, (raw[:cutoff - 1, 3] + 5000) // 10000]
    opening = view['raw_open'][:cutoff]
    adjusted_open = adjusted[:cutoff, 0]
    with np.errstate(divide='ignore', invalid='ignore'):
        gap = opening / previous_raw_close - 1
        factor_error = np.abs((adjusted_open / opening) / (prior_close / previous_raw_close) - 1)
        tolerance = 0.002 + 0.0051 / adjusted_open + 0.0051 / prior_close
    if calculated is None:
        calculated = indicators.calculate(adjusted[:cutoff], valid)
    prior_dmi = np.r_[np.nan, calculated['dmi_difference'][:-1]]
    prior_k = np.r_[np.nan, calculated['kdj_k'][:-1]]
    prior_rsi = np.r_[np.nan, study.rsi6(close, valid)[:-1]]
    prior_board_known = np.r_[False, known[:cutoff - 1]]
    prior_board_sealed = np.r_[False, sealed[:cutoff - 1]]
    upper = study.limit_cents(previous_raw_close, 20)
    scope = (dates[:cutoff] >= '2021-01-01') & (dates[:cutoff] < '2025-01-01')
    mask = (scope & prior_valid & complete_previous & prior_board_known
            & (previous_raw_close >= 100) & (raw[:cutoff, 0] > 0)
            & (raw[:cutoff, 0] % 10000 == 0) & (opening < upper)
            & np.isfinite(adjusted_open) & (adjusted_open > 0)
            & (factor_error <= tolerance) & (gap >= 0.03) & (gap < 0.07)
            & (prior_ma20 > prior_ma60) & (prior_dmi > 0) & np.isfinite(prior_rsi))
    result = []
    for position in np.flatnonzero(mask):
        stage = 'continuation_touch' if prior_board_sealed[position] else 'first_touch'
        result.append((int(position), Watch(code, float(prior_rsi[position]), stage,
                                            bool(np.isfinite(prior_k[position]) and prior_k[position] >= 50))))
    return result, view, known, sealed


def simulate_account(dates, watches, entries, closes, first, last, stage, require_k50,
                     exit_policy='t1', initial=200000.0, cap=20000.0, max_positions=10,
                     slippage=0.002, fee=0.002, annual_cash_yield=0.02, annual_risk_free=0.02,
                     unknown_touches=None):
    if (first < 0 or last >= len(dates) or first > last or stage not in ('first_touch', 'continuation_touch')
            or exit_policy not in ('t1', 'holding') or not math.isfinite(initial) or initial <= 0
            or not math.isfinite(cap) or cap <= 0 or max_positions < 1
            or not math.isfinite(slippage) or not 0 <= slippage < 1
            or not math.isfinite(fee) or not 0 <= fee < 1
            or not math.isfinite(annual_cash_yield) or annual_cash_yield < 0):
        raise ValueError('invalid portfolio assumptions')
    daily_rate = math.expm1(math.log1p(annual_cash_yield) / 252)
    cash = float(initial)
    interest_total = 0.0
    positions = {}
    daily = []
    trades = []
    stale_valuations = []
    unresolved_entries = []
    unknown_touch_entries = []
    unknown_touches = set(unknown_touches or ())
    counters = {'watch_candidates': 0, 'reservations': 0, 'unfilled_no_touch': 0,
                'unfilled_st': 0, 'skipped_duplicate': 0, 'skipped_capacity': 0,
                'unresolved_exit_entries': 0, 'stale_position_days': 0, 'unknown_touch': 0}
    for day in range(first, last + 1):
        interest = cash * daily_rate
        cash += interest
        interest_total += interest
        for code, holding in list(positions.items()):
            if holding['exit'] == day:
                proceeds = holding['units'] * holding['sale'] * (1 - slippage) - holding['budget'] * fee
                cash += proceeds
                trades.append({'code': code, 'entry': holding['entry'], 'exit': day,
                               'budget': holding['budget'], 'pnl': proceeds - holding['budget']})
                del positions[code]
        candidates = [item for item in watches.get(day, [])
                      if item.stage == stage and (not require_k50 or item.k50)]
        if len({item.code for item in candidates}) != len(candidates):
            raise ValueError('duplicate premarket candidate')
        if any(not math.isfinite(item.rsi) for item in candidates):
            raise ValueError('candidate priority must be known before the session')
        candidates.sort(key=lambda item: (item.rsi, item.code))
        counters['watch_candidates'] += len(candidates)
        reserved_cash = cash
        slots = max_positions - len(positions)
        reservations = []
        for item in candidates:
            if item.code in positions:
                counters['skipped_duplicate'] += 1
                continue
            if slots <= 0 or reserved_cash <= 1e-8:
                counters['skipped_capacity'] += 1
                continue
            budget = min(cap, reserved_cash)
            reservations.append((item, budget))
            slots -= 1
            reserved_cash -= budget
        counters['reservations'] += len(reservations)
        for item, budget in reservations:
            entry = entries.get((day, item.code))
            if entry is None:
                if (day, item.code) in unknown_touches:
                    counters['unknown_touch'] += 1
                    unknown_touch_entries.append({'date': str(dates[day]), 'code': item.code})
                else:
                    counters['unfilled_no_touch'] += 1
                continue
            if (day, item.code) in unknown_touches:
                raise ValueError('unknown touch cannot also have a confirmed entry')
            if entry.code != item.code or entry.day != day:
                raise ValueError('event does not match its premarket reservation')
            if entry.is_st:
                counters['unfilled_st'] += 1
                continue
            if not math.isfinite(entry.price) or entry.price <= 0 or item.code not in closes:
                raise ValueError('entry lacks a valid price or valuation history')
            if exit_policy == 't1':
                exit_day, sale = entry.fixed_exit, entry.fixed_sale
            else:
                exit_day, sale = entry.holding_exit, entry.holding_sale
            if exit_day is None:
                counters['unresolved_exit_entries'] += 1
                unresolved_entries.append({'date': str(dates[day]), 'code': item.code})
            elif exit_day <= day or sale is None or not math.isfinite(sale) or sale <= 0:
                raise ValueError('invalid or same-day sale')
            cash -= budget
            positions[item.code] = {'entry': day, 'budget': budget,
                                    'units': budget / (entry.price * (1 + slippage)),
                                    'exit': exit_day, 'sale': sale, 'last': entry.price}
        equity = cash
        for code, holding in positions.items():
            price = closes[code][day]
            if np.isfinite(price) and price > 0:
                holding['last'] = float(price)
            else:
                counters['stale_position_days'] += 1
                stale_valuations.append({'date': str(dates[day]), 'code': code})
            equity += holding['units'] * holding['last'] - holding['budget'] * fee
        if cash < -1e-7 or not math.isfinite(equity) or equity <= 0 or len(positions) > max_positions:
            raise ValueError('account conservation invariant failed')
        daily.append({'date': str(dates[day]), 'equity': equity, 'cash': cash, 'positions': len(positions)})
    metrics = account_metrics(daily, initial, annual_risk_free)
    raw_sharpe = metrics['sharpe']
    incomplete = (counters['unresolved_exit_entries'] > 0 or counters['stale_position_days'] > 0
                  or counters['unknown_touch'] > 0)
    if incomplete:
        metrics['sharpe'] = None
    profits = np.asarray([trade['pnl'] for trade in trades], dtype=float)
    winners = profits[profits > 0]
    losers = profits[profits < 0]
    metrics.update({
        'diagnostic_sharpe_before_data_gate': raw_sharpe,
        'data_complete': not incomplete,
        'closed_trades': len(trades), 'open_positions': len(positions),
        'wins': len(winners), 'losses': len(losers),
        'win_rate': float(len(winners) / len(profits)) if len(profits) else None,
        'mean_trade_return': float(np.mean([trade['pnl'] / trade['budget'] for trade in trades])) if trades else None,
        'mean_gain': float(np.mean(winners)) if len(winners) else None,
        'mean_loss': float(np.mean(losers)) if len(losers) else None,
        'payoff': float(np.mean(winners) / -np.mean(losers)) if len(winners) and len(losers) else None,
        'pf': float(np.sum(winners) / -np.sum(losers)) if len(losers) else None,
        'final_equity': float(equity), 'cash_interest': float(interest_total), 'counters': counters,
        'stale_valuations': stale_valuations, 'unresolved_entries': unresolved_entries,
        'unknown_touches': unknown_touch_entries,
    })
    return metrics, daily, trades


def verify_frozen_inputs(arguments):
    metadata = json.loads((arguments.output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != study.EVENT_SCHEMA_VERSION
            or metadata.get('limited') or not metadata.get('database_sha256')):
        raise ValueError('account replay requires a full verified extraction')
    checks = ((arguments.database, metadata['database_sha256']),
              (arguments.calendar, metadata['calendar_sha256']),
              (arguments.manifest, metadata['manifest_sha256']),
              (arguments.output / 'events.npz', metadata['events_sha256']))
    if any(study.digest_file(path) != expected for path, expected in checks):
        raise ValueError('frozen input hash mismatch')
    records, statuses, _, evidence = win_analysis.load_evidence(arguments, metadata)
    board_sha = study.digest_file(arguments.board_reference)
    board = json.loads(arguments.board_reference.read_text(encoding='utf-8'))
    if (board.get('events_sha256') != metadata['events_sha256']
            or board.get('database_sha256') != metadata['database_sha256']
            or board.get('evidence') != evidence
            or board.get('holdout_signals_examined') is not False):
        raise ValueError('locked board reference provenance mismatch')
    evidence['board_reference_sha256'] = board_sha
    return metadata, records, statuses, board, evidence


def verify_summary(actual, reference):
    for name, value in actual.items():
        if name not in reference:
            raise ValueError(f'locked reference lacks metric: {name}')
        expected = reference[name]
        if value is None or expected is None:
            matches = value is expected
        else:
            matches = np.isclose(value, expected, rtol=0, atol=1e-10)
        if not matches:
            raise ValueError(f'locked reference differs at metric: {name}')


def replay_entry(code, position, view, known, sealed, is_st, frozen_ratio, frozen_delay):
    if (not 0 <= position < len(known) or not known[position]
            or view['raw_open'][position] <= 0 or type(is_st) is not bool):
        raise ValueError('invalid entry or historical status')
    entry_price = (study.limit_cents(view['previous_close'][position], 20)
                   * view['adj_open'][position] / view['raw_open'][position])
    fixed_ratio = exits.fixed_open_ratio(view, position, entry_price, 1)
    if not np.isclose(fixed_ratio, frozen_ratio, rtol=0, atol=1e-10, equal_nan=True):
        raise ValueError('fixed exit ratio differs from frozen event')
    sellable = (view['normal'][position + 1:]
                & (view['raw_open'][position + 1:] > view['down_five'][position + 1:]))
    available = np.flatnonzero(sellable)
    delay = int(available[0]) if len(available) else -1
    if delay != frozen_delay:
        raise ValueError('fixed exit delay differs from frozen event')
    fixed_exit = position + 1 + delay if delay >= 0 else None
    fixed_sale = float(view['adj_open'][fixed_exit]) if fixed_exit is not None else None
    holding_ratio, holding_delay, target, _ = board_analysis.hold_until_break_exit(
        view, known, sealed, position, 20)
    holding_exit = position + target + holding_delay if np.isfinite(holding_ratio) else None
    holding_sale = float(view['adj_open'][holding_exit]) if holding_exit is not None else None
    if holding_sale is not None and not np.isclose(holding_sale / entry_price, holding_ratio, rtol=0, atol=1e-10):
        raise ValueError('holding exit ratio differs from the price path')
    entry = Entry(code, position, is_st, float(entry_price), fixed_exit, fixed_sale, holding_exit, holding_sale)
    return entry, float(holding_ratio), int(holding_delay)


def replay_market(arguments, events, dates, records, statuses):
    date_index = {str(date): position for position, date in enumerate(dates)}
    cutoff = int(np.searchsorted(dates, '2025-01-01'))
    manifest = study.load_manifest(arguments.manifest)
    event_indices = defaultdict(list)
    keys = set()
    for index in range(len(events['date'])):
        key = (str(events['code'][index]), str(events['date'][index]))
        if key in keys or key[1] not in date_index:
            raise ValueError('duplicate event or missing calendar session')
        keys.add(key)
        event_indices[key[0]].append(index)
    if not set(records) <= keys or set(records) != set(statuses):
        raise ValueError('frozen candidate or status coverage is incomplete')
    count = len(events['date'])
    features = {name: np.zeros(count, dtype=bool) for name in ('non_st', 'k50', 'previous_known', 'previous_sealed')}
    holding_ratio = np.full(count, np.nan)
    holding_delay = np.full(count, -1, dtype=np.int16)
    watches = defaultdict(list)
    entries = {}
    closes = {}
    seen = set()
    missing = []
    unknown_touches = set()
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    try:
        stocks = [row[0] for row in connection.execute('SELECT code FROM stock WHERE active=1 ORDER BY code')
                  if study.limit_board(row[0]) == 20]
        if not set(event_indices) <= set(stocks):
            raise ValueError('development events contain stocks outside the frozen universe')
        for number, code in enumerate(stocks, start=1):
            path = arguments.qfq_dir / (code + '.json.gz')
            if not path.exists():
                missing.append(code)
                continue
            if path.name not in manifest:
                raise ValueError('missing frozen adjusted-price manifest entry')
            raw = study.read_raw(connection, code, date_index)
            adjusted = study.read_adjusted(path, code, date_index, manifest[path.name], str(dates[-1]))
            view = exits.price_view(raw, adjusted)
            calculated = indicators.calculate(adjusted[:cutoff], view['valid'][:cutoff])
            candidates, _, known, sealed = premarket_watches(code, dates, raw, adjusted, view, calculated)
            by_day = dict(candidates)
            if len(by_day) != len(candidates):
                raise ValueError('duplicate premarket watch for stock and date')
            for position, item in candidates:
                watches[position].append(item)
            for index in event_indices.get(code, ()):
                date = str(events['date'][index])
                position = date_index[date]
                if not 0 < position < cutoff:
                    raise ValueError('event is outside development signal dates')
                key = (code, date)
                prior_dmi = calculated['dmi_difference'][position - 1]
                candidate = bool(np.isfinite(prior_dmi) and prior_dmi > 0)
                if candidate != (key in records):
                    raise ValueError('growth candidates differ from locked prior-DMI filter')
                if not candidate:
                    if position in by_day:
                        raise ValueError('watch unexpectedly qualifies a non-candidate event')
                    continue
                prices = raw[position, :4]
                if np.any(prices % 10000 != 0) or (prices // 10000).tolist() != records[key]['daily_cents']:
                    raise ValueError('candidate prices differ from frozen evidence')
                if not known[position] or bool(sealed[position]) != bool(events['closed_at_limit'][index]):
                    raise ValueError('entry-day board differs from frozen event')
                previous_known = bool(known[position - 1])
                previous_sealed = bool(sealed[position - 1])
                if previous_known and (not events['prior_reference_valid'][index]
                                       or previous_sealed != bool(events['prior_limit'][index])):
                    raise ValueError('previous board differs from frozen event')
                prior_k = calculated['kdj_k'][position - 1]
                features['non_st'][index] = not statuses[key]
                features['k50'][index] = bool(np.isfinite(prior_k) and prior_k >= 50)
                features['previous_known'][index] = previous_known
                features['previous_sealed'][index] = previous_sealed
                watch = by_day.get(position)
                rsi = events['rsi6'][index]
                if previous_known and np.isfinite(rsi):
                    if (watch is None or watch.k50 != features['k50'][index]
                            or (watch.stage == 'continuation_touch') != previous_sealed
                            or not np.isclose(watch.rsi, rsi, rtol=0, atol=1e-9)):
                        raise ValueError('premarket watch differs from the frozen event')
                elif watch is not None:
                    raise ValueError('unknown prior board or RSI cannot form a watch')
                entry, holding_ratio[index], holding_delay[index] = replay_entry(
                    code, position, view, known, sealed, statuses[key],
                    events['sale_ratio'][index, study.HOLDS.index(1)],
                    int(events['delay'][index, study.HOLDS.index(1)]))
                entries[(position, code)] = entry
                if code not in closes:
                    closes[code] = np.where(view['valid'], adjusted[:, 3], np.nan)
                seen.add(key)
            for position, _ in candidates:
                key = (position, code)
                if not known[position]:
                    unknown_touches.add(key)
                else:
                    high = (raw[position, 1] + 5000) // 10000
                    upper = study.limit_cents(view['previous_close'][position], 20)
                    if high == upper and key not in entries:
                        raise ValueError('known touch is missing from the frozen candidate events')
            if number % 250 == 0 or number == len(stocks):
                print(f'已扫描 {number}/{len(stocks)} 只创业板/科创板股票', flush=True)
    finally:
        connection.close()
    if seen != set(records):
        raise ValueError('candidate coverage differs from locked replay')
    return watches, entries, closes, features, holding_ratio, holding_delay, unknown_touches, {
        'active_growth_stocks': len(stocks), 'scanned_growth_stocks': len(stocks) - len(missing),
        'missing_adjusted_stocks': missing, 'candidate_events': len(seen),
        'non_st_events': sum(not value for value in statuses.values()),
        'watch_candidates': sum(len(value) for value in watches.values()),
        'unknown_watch_sessions': len(unknown_touches),
    }


def verify_board_results(events, features, holding_ratio, holding_delay, board):
    holding_events = {name: values.copy() if name in ('sale_ratio', 'delay') else values
                      for name, values in events.items()}
    holding_events['sale_ratio'][:, study.HOLDS.index(1)] = holding_ratio
    holding_events['delay'][:, study.HOLDS.index(1)] = holding_delay
    periods = analysis.period_masks(events['date'])
    groups = {'first_touch': features['previous_known'] & ~features['previous_sealed'],
              'continuation_touch': features['previous_known'] & features['previous_sealed']}
    checked = 0
    for cohort, require_k50 in (('growth_non_st_base', False), ('growth_non_st_k_ge_50', True)):
        selected = features['non_st'] & (features['k50'] if require_k50 else True)
        for stage, group in groups.items():
            for period_name in board_analysis.PRIMARY_PERIODS:
                mask = selected & group & periods[period_name]
                expected = board['cohorts'][cohort][stage]['periods'][period_name]
                for current, policy in ((events, 't_plus_1'), (holding_events, 'hold_until_break_max_5')):
                    verify_summary(study.summarize(current, mask, 1, 0.002), expected[policy]['20bp'])
                    checked += 1
    return checked


def run(arguments):
    started = time.monotonic()
    if arguments.result.exists():
        raise ValueError('refusing to overwrite account research result')
    metadata, records, statuses, board, evidence = verify_frozen_inputs(arguments)
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines()
                        if line.strip()])
    if (len(dates) < 100 or len(dates) != len(np.unique(dates)) or np.any(dates[1:] <= dates[:-1])
            or dates[0] >= '2021-01-01' or dates[-1] <= '2025-01-01'):
        raise ValueError('trading calendar lacks ordered development and exit coverage')
    all_events = study.load_events(arguments.output / 'events.npz')
    mask = ((all_events['date'] >= '2021-01-01') & (all_events['date'] < '2025-01-01')
            & study.select_mask(all_events, minute_sample.BASE_CONFIG))
    events = {name: values[mask] for name, values in all_events.items()}
    if not len(events['date']):
        raise ValueError('development events are unavailable')
    watches, entries, closes, features, holding_ratio, holding_delay, unknown_touches, coverage = replay_market(
        arguments, events, dates, records, statuses)
    checked = verify_board_results(events, features, holding_ratio, holding_delay, board)
    scenarios = []
    for period_name, start, end in (('training_2021_2023', '2021-01-01', '2024-01-01'),
                                    ('selection_2024', '2024-01-01', '2025-01-01')):
        first = int(np.searchsorted(dates, start))
        last = int(np.searchsorted(dates, end)) - 1
        for stage in ('first_touch', 'continuation_touch'):
            for require_k50 in (False, True):
                for exit_policy in ('t1', 'holding'):
                    for cash_yield in (0.02, 0.0):
                        for slippage in (0.002, 0.003):
                            metrics, daily, trades = simulate_account(
                                dates, watches, entries, closes, first, last, stage, require_k50,
                                exit_policy=exit_policy, annual_cash_yield=cash_yield, slippage=slippage,
                                unknown_touches=unknown_touches)
                            dated_trades = [{**trade, 'entry_date': str(dates[trade['entry']]),
                                             'exit_date': str(dates[trade['exit']])} for trade in trades]
                            scenarios.append({'period': period_name, 'stage': stage, 'k50': require_k50,
                                              'exit': exit_policy, 'annual_cash_yield': cash_yield,
                                              'slippage_per_side': slippage, 'metrics': metrics,
                                              'daily': daily, 'trades': dated_trades})
    verify_frozen_inputs(arguments)
    if study.digest_file(arguments.board_reference) != evidence['board_reference_sha256']:
        raise ValueError('board reference changed during analysis')
    result = {
        'schema_version': 1,
        'inputs': {'database_sha256': metadata['database_sha256'], 'calendar_sha256': metadata['calendar_sha256'],
                   'manifest_sha256': metadata['manifest_sha256'], 'events_sha256': metadata['events_sha256'],
                   'evidence': evidence},
        'coverage': coverage, 'board_reference_summaries_checked': checked,
        'holdout_signals_examined': False,
        'assumptions': {
            'initial': 200000, 'position_cap': 20000, 'max_positions': 10,
            'annual_risk_free': 0.02, 'round_trip_fee_on_budget': 0.002,
            'daily_returns': 'simple equity returns, sample standard deviation, annualized by sqrt(252)',
            'ranking': 'after opening and before touch, previous RSI6 ascending then code; reserve capacity',
            'non_touch_st': 'unknown; reservations not reused for non-touch or ST candidates',
            'unknown_touches': 'unknown daily board data gates account Sharpe if its watch was reserved',
            'execution': 'all qualifying intraday touches fill at estimated board price; no queue, volume or lot model',
            'exit': 'first permitted open on T+1 or following first unsealed close capped at T+5; 5% downside proxy',
            'valuation': 'adjusted daily closes; estimated round-trip fees accrued while position is open',
            'window': 'separately reset accounts; mark remaining holdings at period end without forcing exit',
            'cash': 'cash interest accrues before each session using (1+annual_yield)^(1/252)-1',
        },
        'scenarios': scenarios,
    }
    write_result(arguments.result, result)
    print(json.dumps({'scenarios': len(scenarios), 'coverage': coverage,
                      'board_reference_summaries_checked': checked,
                      'elapsed_seconds': round(time.monotonic() - started, 2)}, ensure_ascii=False), flush=True)


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
    parser.add_argument('--board-reference', type=pathlib.Path, required=True)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    run(parser.parse_args())


if __name__ == '__main__':
    main()
