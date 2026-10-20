package storage

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// MinuteImportResult describes a non-destructive archive merge. Existing
// realtime candles are kept; archive candles only fill missing timestamps.
type MinuteImportResult struct {
	Source   string         `json:"source"`
	Inserted int            `json:"inserted"`
	Existing int            `json:"existing"`
	Mismatch int            `json:"overlap_mismatch"`
	DayBars  map[string]int `json:"day_bars"`
}

// ImportMinuteArchive merges a completed historical minute range into the
// existing realtime series. It never deletes or overwrites a timestamp that is
// already present, so a Baostock refresh cannot replace a Tencent observation.
func (m *MarketDB) ImportMinuteArchive(ctx context.Context, stock config.Stock, archive market.MinuteArchive) (MinuteImportResult, error) {
	out := MinuteImportResult{Source: archive.Source, DayBars: map[string]int{}}
	if err := m.checkWriter(ctx); err != nil {
		return out, err
	}
	if archive.Code != stock.Code || !config.ValidCode(stock.Code) {
		return out, errors.New("minute archive identity mismatch")
	}
	if err := archive.Validate(); err != nil {
		return out, err
	}
	if len(archive.Bars) == 0 {
		return out, nil
	}
	first, _ := time.ParseInLocation("2006-01-02", archive.From, market.Shanghai)
	last, _ := time.ParseInLocation("2006-01-02", archive.Through, market.Shanghai)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return out, errors.New("cannot start minute archive transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO stock(code,name,sector,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(code) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE stock.name END,
		sector=CASE WHEN excluded.sector<>'' THEN excluded.sector ELSE stock.sector END,updated_at=excluded.updated_at`,
		stock.Code, stock.Name, stock.Sector, archive.FetchedAt.Format(time.RFC3339Nano)); err != nil {
		return out, errors.New("cannot persist minute archive stock")
	}
	existing := map[int64]storedMinuteBar{}
	rows, err := tx.QueryContext(ctx, `SELECT end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final
		FROM intraday_bar WHERE code=? AND end_unix>=? AND end_unix<=?`, stock.Code, first.Unix(), last.AddDate(0, 0, 1).Unix()-1)
	if err != nil {
		return out, errors.New("cannot read minute archive overlap")
	}
	for rows.Next() {
		var bar storedMinuteBar
		if err := rows.Scan(&bar.end, &bar.open, &bar.high, &bar.low, &bar.close, &bar.volume, &bar.final); err != nil {
			rows.Close()
			return out, errors.New("cannot decode minute archive overlap")
		}
		existing[bar.end] = bar
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, errors.New("cannot read minute archive overlap")
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO intraday_bar(code,end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final)
		VALUES(?,?,?,?,?,?,?,1)`)
	if err != nil {
		return out, errors.New("cannot prepare minute archive write")
	}
	defer stmt.Close()
	for _, bar := range archive.Bars {
		volume := bar.Volume
		if archive.VolumeUnit == "shares" {
			volume = market.TencentMinuteVolumeRaw(stock.Code, volume)
		}
		stored := storedMinuteBar{
			end: bar.End.Unix(), open: micros(bar.Open), high: micros(bar.High), low: micros(bar.Low),
			close: micros(bar.Close), volume: volume, final: 1,
		}
		if prior, ok := existing[stored.end]; ok {
			if prior.final == 1 {
				out.DayBars[bar.End.In(market.Shanghai).Format("2006-01-02")]++
			}
			if !sameMinuteBar(prior, stored) {
				out.Mismatch++
			} else {
				out.Existing++
			}
			continue
		}
		if _, err := stmt.ExecContext(ctx, stock.Code, stored.end, stored.open, stored.high, stored.low, stored.close, stored.volume); err != nil {
			return out, errors.New("cannot import minute archive bar")
		}
		existing[stored.end] = stored
		out.Inserted++
		out.DayBars[bar.End.In(market.Shanghai).Format("2006-01-02")]++
	}
	if err := tx.Commit(); err != nil {
		return out, errors.New("cannot commit minute archive")
	}
	return out, nil
}

type storedMinuteBar struct {
	end, open, high, low, close int64
	volume                      float64
	final                       int
}

func sameMinuteBar(a, b storedMinuteBar) bool {
	return a.final == b.final && absInt(a.open-b.open) <= 1_000 && absInt(a.high-b.high) <= 1_000 &&
		absInt(a.low-b.low) <= 1_000 && absInt(a.close-b.close) <= 1_000 &&
		math.Abs(a.volume-b.volume) <= math.Max(0.5, math.Abs(b.volume)*1e-6)
}

func absInt(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// Daily trade dates bound the expected minute sessions for a stock. A day with
// no daily bar is not assumed to require a minute session (listing/suspension).
func (m *MarketDB) LoadDailyDates(ctx context.Context, codes []string, from, through, adjust string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if m == nil || m.db == nil || len(codes) == 0 || adjust == "" || from > through {
		return out, errors.New("invalid daily date request")
	}
	args := make([]any, 0, len(codes)+3)
	for _, code := range codes {
		args = append(args, code)
	}
	args = append(args, adjust, from, through)
	rows, err := m.db.QueryContext(ctx, `SELECT code,trade_date FROM daily_bar WHERE code IN (`+placeholders(len(codes))+`)
		AND adjust=? AND trade_date>=? AND trade_date<=? ORDER BY code,trade_date`, args...)
	if err != nil {
		return out, errors.New("cannot read daily dates")
	}
	defer rows.Close()
	for rows.Next() {
		var code, day string
		if err := rows.Scan(&code, &day); err != nil {
			return out, errors.New("cannot decode daily dates")
		}
		if out[code] == nil {
			out[code] = map[string]bool{}
		}
		out[code][day] = true
	}
	if err := rows.Err(); err != nil {
		return out, errors.New("cannot read daily dates")
	}
	return out, nil
}

// LoadMinuteDayCounts returns finalized native-bar counts by Shanghai session
// date. It is used to resume a backfill without re-downloading complete stocks.
func (m *MarketDB) LoadMinuteDayCounts(ctx context.Context, code, from, through string) (map[string]int, error) {
	out := map[string]int{}
	start, e1 := time.ParseInLocation("2006-01-02", from, market.Shanghai)
	end, e2 := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if m == nil || m.db == nil || !config.ValidCode(code) || e1 != nil || e2 != nil || start.After(end) || end.Sub(start) > 20*366*24*time.Hour {
		return out, errors.New("invalid minute day-count request")
	}
	rows, err := m.db.QueryContext(ctx, `SELECT end_unix FROM intraday_bar WHERE code=? AND final=1 AND end_unix>=? AND end_unix<?`,
		code, start.Unix(), end.AddDate(0, 0, 1).Unix())
	if err != nil {
		return out, errors.New("cannot read minute day counts")
	}
	defer rows.Close()
	for rows.Next() {
		var end int64
		if err := rows.Scan(&end); err != nil {
			return out, errors.New("cannot decode minute day count")
		}
		out[time.Unix(end, 0).In(market.Shanghai).Format("2006-01-02")]++
	}
	if err := rows.Err(); err != nil {
		return out, errors.New("cannot read minute day counts")
	}
	return out, nil
}
