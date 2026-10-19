package storage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"io/fs"
	"time"
)

// LoadMinutesRange reads archived native candles for research, inclusively by
// Shanghai session date. Display and provider page limits do not apply. Missing
// sessions are left missing; callers must enforce their own coverage contract.
func (m *MarketDB) LoadMinutesRange(ctx context.Context, stock config.Stock, from, through string) (market.MinuteHistory, error) {
	var out market.MinuteHistory
	start, e1 := time.ParseInLocation("2006-01-02", from, market.Shanghai)
	end, e2 := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if e1 != nil || e2 != nil || start.After(end) || end.Sub(start) > 20*366*24*time.Hour || !config.ValidCode(stock.Code) {
		return out, errors.New("invalid minute archive range (maximum 20 years)")
	}
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	state, err := minuteState(ctx, tx, stock.Code)
	if err != nil {
		return out, err
	}
	if state.FetchedAt == nil {
		return out, fs.ErrNotExist
	}
	out = market.MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: *state.FetchedAt}
	rows, err := tx.QueryContext(ctx, `SELECT end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final FROM intraday_bar WHERE code=? AND end_unix>=? AND end_unix<? ORDER BY end_unix LIMIT ?`, stock.Code, start.Unix(), end.AddDate(0, 0, 1).Unix(), market.MinuteArchiveLimit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b market.MinuteBar
		var ts, o, h, l, c int64
		if err := rows.Scan(&ts, &o, &h, &l, &c, &b.Volume, &b.Final); err != nil {
			rows.Close()
			return out, err
		}
		b.End = time.Unix(ts, 0).In(market.Shanghai)
		b.Open, b.High, b.Low, b.Close = float64(o)/1e6, float64(h)/1e6, float64(l)/1e6, float64(c)/1e6
		out.Bars = append(out.Bars, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Bars) == 0 {
		return out, fs.ErrNotExist
	}
	if err := out.ValidateArchive(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
