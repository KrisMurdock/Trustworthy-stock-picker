#!/usr/bin/env python3
"""Offline daily-bar limit-up event research; never places orders."""

import argparse
import gzip
import hashlib
import itertools
import json
import pathlib
import sqlite3
import time

import numpy as np
from scipy.signal import lfilter


HOLDS = (1, 2, 3, 5)
FEE = 0.002
EVENT_SCHEMA_VERSION = 2
FIRST_DATE = '2021-01-01'
LAST_DATE = '2026-09-30'
MAIN_PREFIXES = ('sh600', 'sh601', 'sh603', 'sh605', 'sz000', 'sz001', 'sz002', 'sz003')
GROWTH_PREFIXES = ('sh688', 'sz300', 'sz301', 'sz302')


def digest_file(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def limit_cents(previous_cents, percent):
    prices = np.asarray(previous_cents, dtype=np.int64)
    result = (prices * (100 + percent) + 50) // 100
    if percent > 0:
        return np.maximum(result, prices + 1)
    return np.minimum(result, prices - 1)


def rolling_mean(values, valid, width):
    values = np.asarray(values, dtype=float)
    valid = np.asarray(valid, dtype=bool)
    totals = np.r_[0.0, np.cumsum(np.where(valid, values, 0.0))]
    counts = np.r_[0, np.cumsum(valid.astype(np.int64))]
    result = np.full(len(values), np.nan)
    if len(values) >= width:
        sums = totals[width:] - totals[:-width]
        complete = counts[width:] - counts[:-width] == width
        result[width - 1:] = np.where(complete, sums / width, np.nan)
    return result


def rsi6(close, valid):
    close = np.asarray(close, dtype=float)
    valid = np.asarray(valid, dtype=bool)
    result = np.full(len(close), np.nan)
    changes = np.diff(np.r_[False, valid, False].astype(np.int8))
    starts = np.flatnonzero(changes == 1)
    ends = np.flatnonzero(changes == -1)
    for start, end in zip(starts, ends):
        if end - start < 2:
            continue
        movement = np.diff(close[start:end])
        gains = np.maximum(movement, 0.0)
        magnitude = np.abs(movement)
        smooth_gain = np.r_[gains[0], lfilter([1 / 6], [1, -5 / 6], gains[1:], zi=[5 * gains[0] / 6])[0]]
        smooth_magnitude = np.r_[magnitude[0], lfilter([1 / 6], [1, -5 / 6], magnitude[1:], zi=[5 * magnitude[0] / 6])[0]]
        result[start + 1:end] = np.divide(100 * smooth_gain, smooth_magnitude,
                                          out=np.full(len(gains), np.nan), where=smooth_magnitude > 0)
    return result


def next_available(sellable):
    indexes = np.arange(len(sellable), dtype=np.int32)
    possible = np.where(sellable, indexes, len(sellable))
    return np.minimum.accumulate(possible[::-1])[::-1]


def limit_board(code):
    if code.startswith(MAIN_PREFIXES):
        return 10
    if code.startswith(GROWTH_PREFIXES):
        return 20
    return None


def load_manifest(path):
    with gzip.open(path, 'rt', encoding='utf-8') as stream:
        records = [json.loads(line) for line in stream]
    result = {}
    for record in records:
        name = record['name']
        if pathlib.Path(name).name != name or name in result or not record.get('equal'):
            raise ValueError('invalid or duplicate frozen price manifest entry')
        result[name] = record['frozen_sha256']
    return result


def read_adjusted(path, code, date_index, expected_sha, last_date):
    payload = path.read_bytes()
    if hashlib.sha256(payload).hexdigest() != expected_sha:
        raise ValueError(f'frozen price hash mismatch: {code}')
    source = json.loads(gzip.decompress(payload))
    expected = {'schema_version': 1, 'code': code, 'source': 'sina', 'adjust': 'qfq', 'data_field': 'qfqday'}
    if any(source.get(key) != value for key, value in expected.items()) or source.get('as_of', '') < last_date:
        raise ValueError(f'invalid adjusted price metadata: {code}')
    adjusted = np.full((len(date_index), 5), np.nan)
    previous = ''
    for bar in source['bars']:
        date = bar['date']
        if date <= previous:
            raise ValueError(f'duplicate or unordered adjusted bars: {code}')
        previous = date
        index = date_index.get(date)
        if index is not None:
            adjusted[index] = [float(bar[key]) for key in ('open', 'high', 'low', 'close', 'volume_raw')]
    return adjusted


def read_raw(connection, code, date_index):
    raw = np.zeros((len(date_index), 5), dtype=np.int64)
    rows = connection.execute(
        "SELECT trade_date,open_micros,high_micros,low_micros,close_micros,volume_shares,data_field "
        "FROM daily_bar WHERE code=? AND adjust='none' ORDER BY trade_date", (code,))
    for date, opening, high, low, close, volume, field in rows:
        index = date_index.get(date)
        if index is not None and field == 'day' and all(value is not None for value in (opening, high, low, close, volume)):
            raw[index] = [opening, high, low, close, volume]
    return raw


def extract_stock(code, dates, raw, adjusted):
    percent = limit_board(code)
    size = len(dates)
    if percent is None or raw.shape != (size, 5) or adjusted.shape != (size, 5):
        raise ValueError('invalid market or price shape')
    cents = (raw[:, :4] + 5000) // 10000
    raw_open, raw_high, raw_low, raw_close = cents.T
    adj_open, adj_high, adj_low, adj_close, adj_volume = adjusted.T
    raw_valid = (np.all(raw[:, :4] > 0, axis=1) & (raw[:, 4] > 0)
                 & np.all(raw[:, :4] % 10000 == 0, axis=1)
                 & (raw_high >= np.maximum(raw_open, raw_close))
                 & (raw_low <= np.minimum(raw_open, raw_close)) & (raw_high >= raw_low))
    adj_valid = (np.all(np.isfinite(adjusted), axis=1) & np.all(adjusted > 0, axis=1)
                 & (adj_high >= np.maximum(adj_open, adj_close))
                 & (adj_low <= np.minimum(adj_open, adj_close)) & (adj_high >= adj_low))
    with np.errstate(divide='ignore', invalid='ignore'):
        factor_close = adj_close / raw_close
        shape_error = np.maximum.reduce((np.abs(adj_open / (raw_open * factor_close) - 1),
                                         np.abs(adj_high / (raw_high * factor_close) - 1),
                                         np.abs(adj_low / (raw_low * factor_close) - 1)))
    valid = raw_valid & adj_valid & (shape_error <= 0.03)
    available = np.where(valid, np.arange(size), -1)
    previous = np.r_[-1, np.maximum.accumulate(available)[:-1]]
    has_previous = previous >= 0
    previous_safe = np.maximum(previous, 0)
    previous_close = np.where(has_previous, raw_close[previous_safe], 0)
    previous_adjusted = np.where(has_previous, adj_close[previous_safe], np.nan)
    with np.errstate(divide='ignore', invalid='ignore'):
        current_factor = adj_open / raw_open
        previous_factor = previous_adjusted / previous_close
        factor_error = np.abs(current_factor / previous_factor - 1)
        tolerance = 0.002 + 0.0051 / adj_open + 0.0051 / previous_adjusted
    normal_reference = valid & has_previous & (factor_error <= tolerance)
    up_limit = limit_cents(previous_close, percent)
    down_five = limit_cents(previous_close, -5)
    recent_count = rolling_mean(np.ones(size), valid, 60)
    complete_previous = np.r_[False, np.isfinite(recent_count[:-1])]
    mean20 = rolling_mean(adj_close, valid, 20)
    mean60 = rolling_mean(adj_close, valid, 60)
    relative_strength = rsi6(adj_close, valid)
    consecutive = previous == np.arange(size) - 1
    daily_closed_limit = normal_reference & consecutive & (raw_close == up_limit) & (raw_high == up_limit)
    prior_closed_limit = np.r_[False, daily_closed_limit[:-1]]
    date_scope = (dates >= FIRST_DATE) & (dates <= LAST_DATE)
    touch_scope = date_scope & valid & complete_previous & consecutive & (previous_close >= 100)
    touches = touch_scope & normal_reference & (raw_open < up_limit) & (raw_high == up_limit)
    positions = np.flatnonzero(touches)
    sellable = normal_reference & (raw_open > down_five)
    next_exit = next_available(sellable)
    ratios = np.full((len(positions), len(HOLDS)), np.nan)
    delays = np.full((len(positions), len(HOLDS)), -1, dtype=np.int16)
    adjusted_entry = up_limit[positions] * adj_open[positions] / raw_open[positions]
    for hold_index, hold in enumerate(HOLDS):
        target = positions + hold
        within = target < size
        exits = np.full(len(positions), size, dtype=np.int32)
        exits[within] = next_exit[target[within]]
        resolved = exits < size
        ratios[resolved, hold_index] = adj_open[exits[resolved]] / adjusted_entry[resolved]
        delays[resolved, hold_index] = exits[resolved] - target[resolved]
    previous_day = np.maximum(positions - 1, 0)
    prior_reference_valid = normal_reference[previous_day] & consecutive[previous_day]
    result = {
        'code': np.full(len(positions), code, dtype='<U8'),
        'date': dates[positions],
        'board': np.full(len(positions), percent, dtype=np.int8),
        'prior_reference_valid': prior_reference_valid,
        'prior_limit': prior_closed_limit[positions],
        'above_ma20': adj_close[previous_day] > mean20[previous_day],
        'ma20_above_ma60': mean20[previous_day] > mean60[previous_day],
        'rsi6': relative_strength[previous_day],
        'gap': raw_open[positions] / previous_close[positions] - 1,
        'closed_at_limit': raw_close[positions] == up_limit[positions],
        'sale_ratio': ratios,
        'delay': delays,
    }
    quality = {
        'valid_rows': int(valid.sum()),
        'invalid_raw_rows': int(np.count_nonzero((raw[:, 0] > 0) & ~raw_valid)),
        'shape_mismatch_rows': int(np.count_nonzero(raw_valid & adj_valid & (shape_error > 0.03))),
        'suspected_action_rows': int(np.count_nonzero(date_scope & valid & has_previous & ~normal_reference)),
        'open_at_limit_or_higher': int(np.count_nonzero(touch_scope & normal_reference
                                                         & (raw_open >= up_limit) & (raw_high == up_limit))),
        'prior_reference_unavailable_events': int(np.count_nonzero(~prior_reference_valid)),
        'touches': len(positions),
    }
    return result, quality


def combine_batches(batches):
    if not batches:
        raise ValueError('no eligible touch events')
    return {key: np.concatenate([batch[key] for batch in batches]) for key in batches[0]}


def extract(arguments):
    started = time.monotonic()
    output = arguments.output
    output.mkdir(parents=True, exist_ok=True)
    if (output / 'events.npz').exists() or (output / 'extract.json').exists():
        raise ValueError('refusing to overwrite extraction output')
    dates = np.asarray([line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()])
    if len(dates) < 100 or np.any(dates[1:] <= dates[:-1]) or dates[-1] < '2026-10-30':
        raise ValueError('calendar is too short or not strictly increasing')
    date_index = {date: index for index, date in enumerate(dates)}
    manifest = load_manifest(arguments.manifest)
    if arguments.expected_db_sha256:
        actual_sha = digest_file(arguments.database)
        if actual_sha != arguments.expected_db_sha256:
            raise ValueError('frozen database hash mismatch')
    else:
        actual_sha = None
    connection = sqlite3.connect(arguments.database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    stocks = [row[0] for row in connection.execute('SELECT code FROM stock WHERE active=1 ORDER BY code')
              if limit_board(row[0]) is not None]
    total_stocks = len(stocks)
    if arguments.max_stocks:
        stocks = stocks[:arguments.max_stocks]
    quality = {'stocks_in_pool': total_stocks, 'stocks_scanned': 0, 'stocks_with_events': 0,
               'missing_adjusted_stocks': 0}
    batches = []
    try:
        for stock_index, code in enumerate(stocks, start=1):
            price_path = arguments.qfq_dir / (code + '.json.gz')
            if not price_path.exists():
                quality['missing_adjusted_stocks'] += 1
                continue
            expected = manifest.get(price_path.name)
            if not expected:
                raise ValueError(f'missing frozen price manifest entry: {code}')
            raw = read_raw(connection, code, date_index)
            adjusted = read_adjusted(price_path, code, date_index, expected, dates[-1])
            batch, counts = extract_stock(code, dates, raw, adjusted)
            quality['stocks_scanned'] += 1
            for key, value in counts.items():
                quality[key] = quality.get(key, 0) + value
            if len(batch['date']):
                quality['stocks_with_events'] += 1
                batches.append(batch)
            if stock_index % 250 == 0 or stock_index == len(stocks):
                print(f'已扫描 {stock_index}/{len(stocks)} 只，触板事件 {quality.get("touches", 0)}', flush=True)
    finally:
        connection.close()
    events = combine_batches(batches)
    np.savez_compressed(output / 'events.npz', **events)
    metadata = {
        'event_schema_version': EVENT_SCHEMA_VERSION,
        'database_sha256': actual_sha,
        'calendar_sha256': digest_file(arguments.calendar),
        'manifest_sha256': digest_file(arguments.manifest),
        'events_sha256': digest_file(output / 'events.npz'),
        'calendar_first': str(dates[0]), 'calendar_last': str(dates[-1]),
        'first_date': FIRST_DATE, 'last_date': LAST_DATE,
        'limited': bool(arguments.max_stocks), 'quality': quality,
        'elapsed_seconds': round(time.monotonic() - started, 2),
    }
    (output / 'extract.json').write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(metadata, ensure_ascii=False, indent=2), flush=True)


def select_mask(events, config):
    mask = events['board'] == config['board']
    if config['prior'] == 'first':
        mask &= events['prior_reference_valid'] & ~events['prior_limit']
    elif config['prior'] == 'prior_limit':
        mask &= events['prior_reference_valid'] & events['prior_limit']
    elif config['prior'] != 'any':
        raise ValueError('invalid prior-limit condition')
    if config['trend'] == 'above_ma20':
        mask &= events['above_ma20']
    elif config['trend'] == 'ma20_above_ma60':
        mask &= events['ma20_above_ma60']
    elif config['trend'] != 'any':
        raise ValueError('invalid trend condition')
    if config['rsi'] == 'at_least_50':
        mask &= events['rsi6'] >= 50
    elif config['rsi'] == 'at_least_70':
        mask &= events['rsi6'] >= 70
    elif config['rsi'] != 'any':
        raise ValueError('invalid RSI condition')
    if config['gap'] == 'zero_to_three':
        mask &= (events['gap'] >= 0) & (events['gap'] < 0.03)
    elif config['gap'] == 'three_to_seven':
        mask &= (events['gap'] >= 0.03) & (events['gap'] < 0.07)
    elif config['gap'] != 'any':
        raise ValueError('invalid opening-gap condition')
    return mask


def net_returns(ratios, slippage):
    return ratios * (1 - slippage) / (1 + slippage) - 1 - FEE


def summarize(events, mask, hold, slippage=0.002):
    hold_index = HOLDS.index(hold)
    indices = np.flatnonzero(mask)
    ratios = events['sale_ratio'][indices, hold_index]
    resolved = np.isfinite(ratios)
    complete = indices[resolved]
    returns = net_returns(ratios[resolved], slippage)
    gains = returns[returns > 0]
    losses = returns[returns < 0]
    gains_total = float(gains.sum())
    losses_total = float(-losses.sum())
    return {
        'signals': int(len(indices)), 'closed': int(len(complete)), 'unresolved': int(np.count_nonzero(~resolved)),
        'days': int(len(np.unique(events['date'][complete]))),
        'stocks': int(len(np.unique(events['code'][complete]))),
        'wins': int(len(gains)), 'losses': int(len(losses)),
        'win_rate': float(len(gains) / len(returns)) if len(returns) else None,
        'mean_return': float(np.mean(returns)) if len(returns) else None,
        'median_return': float(np.median(returns)) if len(returns) else None,
        'mean_gain': float(np.mean(gains)) if len(gains) else None,
        'mean_loss': float(np.mean(losses)) if len(losses) else None,
        'payoff': float(np.mean(gains) / -np.mean(losses)) if len(gains) and len(losses) else None,
        'pf': gains_total / losses_total if losses_total else None,
        'delayed': int(np.count_nonzero(events['delay'][complete, hold_index] > 0)),
        'finished_at_limit': int(np.count_nonzero(events['closed_at_limit'][indices])),
    }


def eligible(stats, minimum_trades, minimum_days, minimum_stocks):
    return (stats['closed'] >= minimum_trades and stats['days'] >= minimum_days
            and stats['stocks'] >= minimum_stocks and stats['unresolved'] == 0
            and stats['pf'] is not None and stats['payoff'] is not None)


def candidate_score(training, selection):
    return min(training['pf'] / 1.5, training['payoff'] / 1.2,
               selection['pf'] / 1.5, selection['payoff'] / 1.2)


def load_events(path):
    with np.load(path, allow_pickle=False) as source:
        events = {key: source[key] for key in source.files}
    if 'prior_reference_valid' not in events:
        raise ValueError('unsupported event data: prior_reference_valid is missing; re-extract input')
    return events


def find_candidates(events, minimums=(300, 80, 50, 80, 40, 25)):
    years = np.asarray([int(date[:4]) for date in events['date']], dtype=np.int16)
    training_mask = (years >= 2021) & (years <= 2023)
    selection_mask = years == 2024
    minimum_train, minimum_train_days, minimum_train_stocks, minimum_selection, minimum_selection_days, minimum_selection_stocks = minimums
    records = []
    tested = 0
    combinations = itertools.product((10, 20), ('any', 'first', 'prior_limit'),
                                     ('any', 'above_ma20', 'ma20_above_ma60'),
                                     ('any', 'at_least_50', 'at_least_70'),
                                     ('any', 'zero_to_three', 'three_to_seven'), HOLDS)
    for board, prior, trend, rsi, gap, hold in combinations:
        tested += 1
        config = {'board': board, 'prior': prior, 'trend': trend, 'rsi': rsi, 'gap': gap, 'hold': hold}
        chosen = select_mask(events, config)
        training = summarize(events, chosen & training_mask, hold)
        if not eligible(training, minimum_train, minimum_train_days, minimum_train_stocks):
            continue
        selection = summarize(events, chosen & selection_mask, hold)
        if not eligible(selection, minimum_selection, minimum_selection_days, minimum_selection_stocks):
            continue
        score = candidate_score(training, selection)
        records.append({'config': config, 'score': score, 'training': training, 'selection': selection})
    records.sort(key=lambda item: (item['score'], min(item['training']['pf'], item['selection']['pf']),
                                   item['selection']['closed'], item['training']['closed']), reverse=True)
    return tested, records


def train(arguments):
    metadata = json.loads((arguments.output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('event_schema_version') != EVENT_SCHEMA_VERSION
            or metadata['limited'] or not metadata['database_sha256']):
        raise ValueError('selection requires a full, hash-verified extraction')
    path = arguments.output / 'events.npz'
    if digest_file(path) != metadata['events_sha256']:
        raise ValueError('event data hash mismatch')
    events = load_events(path)
    tested, records = find_candidates(events)
    if not records:
        raise ValueError('no candidate met the prespecified sample thresholds')
    passing = sum(record['score'] >= 1 for record in records)
    result = {'events_sha256': metadata['events_sha256'], 'tested': tested,
              'eligible': len(records), 'train_selection_target_passes': passing,
              'minimums': {'training': [300, 80, 50], 'selection': [80, 40, 25]},
              'candidate': records[0], 'top_10': records[:10]}
    destination = arguments.output / 'selection.json'
    if destination.exists():
        raise ValueError('refusing to overwrite frozen selection')
    destination.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)


def bootstrap_pf(events, mask, hold, slippage=0.002, samples=1000):
    hold_index = HOLDS.index(hold)
    indices = np.flatnonzero(mask & np.isfinite(events['sale_ratio'][:, hold_index]))
    if not len(indices):
        return None
    returns = net_returns(events['sale_ratio'][indices, hold_index], slippage)
    dates, inverse = np.unique(events['date'][indices], return_inverse=True)
    if len(dates) < 20:
        return None
    gains = np.bincount(inverse, weights=np.maximum(returns, 0))
    losses = np.bincount(inverse, weights=np.maximum(-returns, 0))
    net = np.bincount(inverse, weights=returns)
    counts = np.bincount(inverse)
    generator = np.random.default_rng(20261110)
    picks = generator.integers(0, len(dates), size=(samples, len(dates)))
    gain_sums = gains[picks].sum(axis=1)
    loss_sums = losses[picks].sum(axis=1)
    averages = net[picks].sum(axis=1) / counts[picks].sum(axis=1)
    valid = loss_sums > 0
    if not np.all(valid):
        return None
    return {'method': 'signal-day clustered bootstrap', 'samples': samples, 'seed': 20261110,
            'pf_95': np.quantile(gain_sums / loss_sums, [0.025, 0.975]).tolist(),
            'mean_return_95': np.quantile(averages, [0.025, 0.975]).tolist()}


def development_exit_cutoff(events, calendar):
    calendar = np.asarray(calendar)
    if not len(calendar) or np.any(calendar[1:] <= calendar[:-1]):
        raise ValueError('invalid frozen calendar')
    development = (events['date'] >= '2021-01-01') & (events['date'] < '2025-01-01')
    dates = events['date'][development]
    if not len(dates):
        raise ValueError('no development events for embargo')
    delays = events['delay'][development]
    ratios = events['sale_ratio'][development]
    if (delays.shape != (len(dates), len(HOLDS)) or ratios.shape != delays.shape
            or np.any(delays < 0) or not np.all(np.isfinite(ratios))):
        raise ValueError('development exits must all be resolved before holdout')
    positions = np.searchsorted(calendar, dates)
    if np.any(positions >= len(calendar)) or np.any(calendar[positions] != dates):
        raise ValueError('development dates are missing from frozen calendar')
    exits = positions[:, None] + np.asarray(HOLDS)[None, :] + delays
    if np.any(exits >= len(calendar)):
        raise ValueError('frozen calendar does not cover development exits')
    return str(calendar[int(np.max(exits))])


def finalize(arguments):
    selection_path = arguments.output / 'selection.json'
    selection = json.loads(selection_path.read_text(encoding='utf-8'))
    if (selection.get('train_selection_target_passes', 0) < 1
            or selection.get('candidate', {}).get('score', 0) < 1):
        raise ValueError('holdout blocked: candidate did not pass the development targets')
    events_path = arguments.output / 'events.npz'
    if digest_file(events_path) != selection['events_sha256']:
        raise ValueError('event data changed after candidate selection')
    metadata = json.loads((arguments.output / 'extract.json').read_text(encoding='utf-8'))
    if (metadata.get('events_sha256') != selection['events_sha256']
            or digest_file(arguments.calendar) != metadata.get('calendar_sha256')):
        raise ValueError('frozen calendar or metadata changed after extraction')
    calendar = [line.strip() for line in arguments.calendar.read_text(encoding='ascii').splitlines() if line.strip()]
    events = load_events(events_path)
    embargo_through = development_exit_cutoff(events, calendar)
    config = selection['candidate']['config']
    chosen = select_mask(events, config)
    years = np.asarray([int(date[:4]) for date in events['date']], dtype=np.int16)
    holdout = (years >= 2025) & (years <= 2026) & (events['date'] > embargo_through)
    periods = {'train_2021_2023': (years >= 2021) & (years <= 2023),
               'selection_2024': years == 2024,
               'holdout_2025_2026': holdout,
               'year_2025': holdout & (years == 2025), 'year_2026': holdout & (years == 2026)}
    results = {}
    for name, period in periods.items():
        current = chosen & period
        results[name] = {'10bp': summarize(events, current, config['hold'], 0.001),
                         '20bp': summarize(events, current, config['hold'], 0.002),
                         '20bp_fried_board_stress': summarize(events, current & ~events['closed_at_limit'], config['hold'], 0.002),
                         '20bp_day_cluster_ci': bootstrap_pf(events, current, config['hold'])}
    baselines = {}
    for board in (10, 20):
        config_base = {'board': board, 'prior': 'any', 'trend': 'any', 'rsi': 'any', 'gap': 'any', 'hold': 1}
        base_mask = select_mask(events, config_base)
        baselines[str(board)] = {name: summarize(events, base_mask & period, 1) for name, period in periods.items()}
    result = {'selection_sha256': digest_file(selection_path), 'events_sha256': selection['events_sha256'],
              'embargo_through': embargo_through, 'candidate': config,
              'periods': results, 'baselines_hold_1': baselines}
    destination = arguments.output / 'holdout.json'
    if destination.exists():
        raise ValueError('refusing to overwrite holdout result')
    destination.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest='command', required=True)
    extraction = subcommands.add_parser('extract')
    extraction.add_argument('--database', type=pathlib.Path, required=True)
    extraction.add_argument('--calendar', type=pathlib.Path, required=True)
    extraction.add_argument('--qfq-dir', type=pathlib.Path, required=True)
    extraction.add_argument('--manifest', type=pathlib.Path, required=True)
    extraction.add_argument('--output', type=pathlib.Path, required=True)
    extraction.add_argument('--expected-db-sha256')
    extraction.add_argument('--max-stocks', type=int, default=0)
    training = subcommands.add_parser('train')
    training.add_argument('--output', type=pathlib.Path, required=True)
    final = subcommands.add_parser('finalize')
    final.add_argument('--output', type=pathlib.Path, required=True)
    final.add_argument('--calendar', type=pathlib.Path, required=True)
    arguments = parser.parse_args()
    if arguments.command == 'extract':
        if arguments.max_stocks < 0:
            parser.error('--max-stocks must not be negative')
        extract(arguments)
    elif arguments.command == 'train':
        train(arguments)
    else:
        finalize(arguments)


if __name__ == '__main__':
    main()
