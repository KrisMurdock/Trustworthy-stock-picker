package storage

import (
	"context"
	"errors"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// LoadMinuteTrainingBars returns finalized native five-minute bars for the
// requested codes in [from, to). It never returns the forming bar.
func (m *MarketDB) LoadMinuteTrainingBars(ctx context.Context, codes []string, from, to time.Time) (map[string][]market.MinuteBar, error) {
	out := map[string][]market.MinuteBar{}
	if m == nil || m.db == nil || len(codes) == 0 || !from.Before(to) {
		return out, errors.New("invalid minute training window")
	}
	args := make([]any, 0, len(codes)+2)
	for _, code := range codes {
		args = append(args, code)
	}
	args = append(args, from.Unix(), to.Unix())
	rows, err := m.db.QueryContext(ctx, `
		SELECT code,end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw
		FROM intraday_bar
		WHERE final=1 AND code IN (`+placeholders(len(codes))+`) AND end_unix>=? AND end_unix<?
		ORDER BY code,end_unix`, args...)
	if err != nil {
		return nil, errors.New("cannot read minute training data")
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var end, open, high, low, closeMicros int64
		var volume float64
		if err := rows.Scan(&code, &end, &open, &high, &low, &closeMicros, &volume); err != nil {
			return nil, errors.New("cannot decode minute training data")
		}
		out[code] = append(out[code], market.MinuteBar{
			End: time.Unix(end, 0).In(market.Shanghai), Open: float64(open) / 1e6,
			High: float64(high) / 1e6, Low: float64(low) / 1e6, Close: float64(closeMicros) / 1e6,
			Volume: volume, Final: true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read minute training data")
	}
	return out, nil
}

// LoadDailyTrainingBars returns adjusted daily bars in [from, through].
func (m *MarketDB) LoadDailyTrainingBars(ctx context.Context, codes []string, from, through, adjust string) (map[string][]market.Bar, error) {
	out := map[string][]market.Bar{}
	if m == nil || m.db == nil || len(codes) == 0 || from == "" || through == "" || adjust == "" {
		return out, errors.New("invalid daily training window")
	}
	args := make([]any, 0, len(codes)+3)
	for _, code := range codes {
		args = append(args, code)
	}
	args = append(args, adjust, from, through)
	rows, err := m.db.QueryContext(ctx, `
		SELECT code,trade_date,open_micros,high_micros,low_micros,close_micros,volume_raw
		FROM daily_bar
		WHERE code IN (`+placeholders(len(codes))+`) AND adjust=? AND trade_date>=? AND trade_date<=?
		ORDER BY code,trade_date`, args...)
	if err != nil {
		return nil, errors.New("cannot read daily training data")
	}
	defer rows.Close()
	for rows.Next() {
		var code, date string
		var open, high, low, closeMicros int64
		var volume float64
		if err := rows.Scan(&code, &date, &open, &high, &low, &closeMicros, &volume); err != nil {
			return nil, errors.New("cannot decode daily training data")
		}
		out[code] = append(out[code], market.Bar{Date: date, Open: float64(open) / 1e6,
			High: float64(high) / 1e6, Low: float64(low) / 1e6, Close: float64(closeMicros) / 1e6, Volume: volume})
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read daily training data")
	}
	return out, nil
}
