package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"stock-watcher/internal/config"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"time"
)

// DashboardSnapshot contains validated market data and independently published reports, never secrets.
type DashboardSnapshot struct {
	ReportCurrent bool              `json:"report_current"`
	Version       int               `json:"schema_version"`
	AsOf          string            `json:"as_of"`
	Target        string            `json:"target_trading_date"`
	GeneratedAt   *time.Time        `json:"generated_at"`
	Adjust        string            `json:"adjust"`
	Summary       DashboardSummary  `json:"summary"`
	Stocks        []DashboardStock  `json:"stocks"`
	Report        *selection.Report `json:"report"`
	LatestSync    *DashboardSync    `json:"latest_sync"`
	Dates         []string          `json:"dates"`
}

type DashboardSummary struct {
	PoolSize      int     `json:"pool_size"`
	Available     int     `json:"available"`
	Missing       int     `json:"missing"`
	Eligible      int     `json:"eligible"`
	Selected      int     `json:"selected"`
	CoveragePct   float64 `json:"coverage_pct"`
	Complete      int     `json:"complete"`
	CompletePct   float64 `json:"complete_pct"`
	Analyzable    int     `json:"analyzable"`
	AnalyzablePct float64 `json:"analyzable_pct"`
	Suspended     int     `json:"suspended"`
	Gaps          int     `json:"gaps"`
}

type DashboardStock struct {
	Source       string              `json:"source"`
	Code         string              `json:"code"`
	Name         string              `json:"name"`
	Sector       string              `json:"sector"`
	Status       string              `json:"status"`
	Reason       string              `json:"reason"`
	LastDate     string              `json:"last_date"`
	Bar          *DashboardBar       `json:"bar"`
	ChangePct    *float64            `json:"change_pct"`
	Metrics      *indicators.Metrics `json:"metrics"`
	Complete     bool                `json:"complete"`
	Analyzable   bool                `json:"analyzable"`
	Sessions     int                 `json:"sessions"`
	Suspended    int                 `json:"suspended"`
	Gaps         int                 `json:"gaps"`
	SessionState string              `json:"session_state,omitempty"`
}

type DashboardBar struct {
	VolumeShares float64 `json:"volume_shares"`
	Date         string  `json:"date"`
	Open         float64 `json:"open"`
	High         float64 `json:"high"`
	Low          float64 `json:"low"`
	Close        float64 `json:"close"`
	Volume       float64 `json:"volume"` // Provider raw volume, matching the persisted history.
}

type DashboardSync struct {
	AsOf          string    `json:"as_of"`
	Status        string    `json:"status"`
	FinishedAt    time.Time `json:"finished_at"`
	CoveragePct   float64   `json:"coverage_pct"`
	AnalyzablePct float64   `json:"analyzable_pct"`
	ErrorKind     string    `json:"error_kind"`
}

// LoadDashboard reads market windows and published reports in one transaction.
// A coverage failure may save newer market data without publishing a new report.
func (m *MarketDB) LoadDashboard(ctx context.Context, stocks []config.Stock, adjust string) (DashboardSnapshot, error) {
	out := DashboardSnapshot{Version: 1, Adjust: adjust, Stocks: make([]DashboardStock, 0, len(stocks)), Dates: []string{}}
	out.Summary.PoolSize, out.Summary.Missing = len(stocks), len(stocks)
	for _, stock := range stocks {
		out.Stocks = append(out.Stocks, DashboardStock{Code: stock.Code, Name: stock.Name, Sector: stock.Sector, Status: "missing", Reason: "missing_data"})
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if m == nil || m.db == nil {
		return out, nil
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, dashboardReadError(ctx)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT as_of FROM selection_run ORDER BY as_of DESC")
	if err != nil {
		return out, dashboardReadError(ctx)
	}
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			rows.Close()
			return out, dashboardReadError(ctx)
		}
		out.Dates = append(out.Dates, date)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, dashboardReadError(ctx)
	}
	var sync DashboardSync
	var finished string
	err = tx.QueryRowContext(ctx, `SELECT as_of, status, finished_at, coverage_pct, analyzable_pct, error_kind
		FROM sync_run ORDER BY julianday(finished_at) DESC, rowid DESC LIMIT 1`).Scan(
		&sync.AsOf, &sync.Status, &finished, &sync.CoveragePct, &sync.AnalyzablePct, &sync.ErrorKind)
	if err == nil {
		sync.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
		if err != nil {
			return out, dashboardReadError(ctx)
		}
		out.LatestSync = &sync
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, dashboardReadError(ctx)
	}
	if len(out.Dates) > 0 {
		report, err := readDashboardReport(ctx, tx, out.Dates[0])
		if err != nil {
			return out, err
		}
		out.Report = &report
	}
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT MAX(as_of) FROM stock_sync WHERE adjust=?", adjust).Scan(&latest); err != nil {
		return out, dashboardReadError(ctx)
	}
	out.AsOf = latest.String
	marketReport := selection.Report{AsOf: out.AsOf, Adjust: adjust}
	var dataRunStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM sync_run WHERE as_of=? AND (status='published' OR error_kind='coverage')
		ORDER BY julianday(finished_at) DESC, rowid DESC LIMIT 1`, out.AsOf).Scan(&dataRunStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, dashboardReadError(ctx)
	}
	out.ReportCurrent = out.Report != nil && out.Report.AsOf == out.AsOf && out.Report.Adjust == adjust && dataRunStatus == "published"
	if out.ReportCurrent {
		marketReport = *out.Report
		out.Target = out.Report.Target
		out.Summary.Eligible, out.Summary.Selected = out.Report.Eligible, len(out.Report.Selected)
	}
	if out.AsOf != "" {
		var fetched sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT MAX(fetched_at) FROM stock_sync WHERE adjust=? AND as_of=? AND status='ok'", adjust, out.AsOf).Scan(&fetched); err != nil {
			return out, dashboardReadError(ctx)
		}
		if fetched.Valid {
			at, err := time.Parse(time.RFC3339Nano, fetched.String)
			if err != nil {
				return out, dashboardReadError(ctx)
			}
			out.GeneratedAt = &at
		}
		for i, stock := range stocks {
			row, err := readDashboardStock(ctx, tx, stock, marketReport)
			if err != nil {
				return out, err
			}
			out.Stocks[i] = row
			if row.Bar != nil {
				out.Summary.Available++
			}
			if row.Complete {
				out.Summary.Complete++
			}
			if row.Analyzable {
				out.Summary.Analyzable++
			}
			if row.Complete && !row.Analyzable && row.SessionState == "suspended" {
				out.Summary.Suspended++
			}
			if row.Gaps > 0 {
				out.Summary.Gaps++
			}
		}
		out.Summary.Missing = len(stocks) - out.Summary.Available
		if len(stocks) > 0 {
			out.Summary.CompletePct = float64(out.Summary.Complete) * 100 / float64(len(stocks))
			out.Summary.AnalyzablePct = float64(out.Summary.Analyzable) * 100 / float64(len(stocks))
		}
		out.Summary.CoveragePct = out.Summary.CompletePct
	}
	if err := tx.Commit(); err != nil {
		return out, dashboardReadError(ctx)
	}
	return out, nil
}

func readDashboardStock(ctx context.Context, reader sqlReader, stock config.Stock, report selection.Report) (DashboardStock, error) {
	row := DashboardStock{Code: stock.Code, Name: stock.Name, Sector: stock.Sector, Status: "missing", Reason: "missing_data"}
	var name, sector string
	var status, reason sql.NullString
	var analyzable, sessions, suspended, gaps sql.NullInt64
	err := reader.QueryRowContext(ctx, `SELECT stock.name, stock.sector, sync.status, sync.reason, sync.analyzable, sync.sessions, sync.suspended, sync.gaps
		FROM stock LEFT JOIN stock_sync AS sync
		ON sync.code=stock.code AND sync.adjust=? AND sync.as_of=? WHERE stock.code=?`,
		report.Adjust, report.AsOf, stock.Code).Scan(&name, &sector, &status, &reason, &analyzable, &sessions, &suspended, &gaps)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return row, dashboardReadError(ctx)
	}
	if row.Name == "" {
		row.Name = name
	}
	if row.Sector == "" {
		row.Sector = sector
	}
	for _, candidate := range report.Selected {
		if candidate.Code == stock.Code && row.Name == "" {
			row.Name = candidate.Name
		}
	}
	for _, candidate := range report.Ranking {
		if candidate.Code == stock.Code && row.Name == "" {
			row.Name = candidate.Name
		}
	}
	row.Analyzable = analyzable.Int64 == 1
	row.Sessions, row.Suspended, row.Gaps = int(sessions.Int64), int(suspended.Int64), int(gaps.Int64)
	row.Complete = row.Gaps == 0 && row.Sessions > 0
	if err := reader.QueryRowContext(ctx, "SELECT state FROM session_status WHERE code=? AND trade_date=?", stock.Code, report.AsOf).Scan(&row.SessionState); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return row, dashboardReadError(ctx)
	}
	if status.String != "ok" {
		if row.Gaps > 0 {
			row.Status = "gap"
		} else if row.SessionState == "suspended" && row.Complete {
			row.Status, row.Reason = "suspended", "complete_not_analyzable"
		} else if status.String != "" {
			row.Status = status.String
		}
		if reason.String != "" {
			row.Reason = reason.String
		}
		for _, excluded := range report.Excluded {
			if excluded.Code == stock.Code {
				row.LastDate = excluded.LastDate
			}
		}
		return row, nil
	}
	stock.Name = row.Name
	if stock.Name == "" {
		stock.Name = stock.Code // Metadata can omit the configured display name.
	}
	history, err := loadHistory(ctx, reader, stock, report.AsOf, report.Adjust)
	if err != nil {
		return row, dashboardReadError(ctx)
	}
	last := history.Bars[len(history.Bars)-1]
	row.Source = history.Source
	row.Bar = &DashboardBar{Date: last.Date, Open: last.Open, High: last.High, Low: last.Low, Close: last.Close, Volume: last.Volume, VolumeShares: market.ProviderVolumeShares(history.Source, history.Code, last.Volume)}
	row.LastDate, row.Status, row.Reason = last.Date, "available", ""
	// Rows written before schema 5 have no completeness counters. Preserve the
	// legacy read contract until the next synchronization fills them.
	if row.Sessions == 0 {
		row.Complete = true
		if last.Date == report.AsOf {
			row.Analyzable = true
		}
	}
	if report.Version > 0 {
		row.Status = "eligible"
	}
	if len(history.Bars) > 1 {
		change := (last.Close/history.Bars[len(history.Bars)-2].Close - 1) * 100
		row.ChangePct = &change
	}
	if metrics, err := indicators.Score(history.Bars); err == nil {
		row.Metrics = &metrics
	}
	for _, excluded := range report.Excluded {
		if excluded.Code == stock.Code {
			row.Status, row.Reason = "excluded", excluded.Reason
		}
	}
	for _, filtered := range report.Filtered {
		if filtered.Code == stock.Code {
			row.Status, row.Reason = "filtered", filtered.Reason
		}
	}
	for _, candidate := range report.Selected {
		if candidate.Code == stock.Code {
			row.Status, row.Reason = "selected", ""
		}
	}
	if row.Gaps > 0 {
		row.Status, row.Reason = "gap", "history_gap"
	} else if !row.Analyzable {
		switch {
		case row.SessionState == "suspended" && row.Complete:
			row.Status, row.Reason = "suspended", "complete_not_analyzable"
		case status.String == "ok":
			row.Status, row.Reason = "isolated", "adjusted_history_unavailable"
		}
	}
	return row, nil
}

// LoadDashboardReport returns the persisted report without reading mutable daily
// windows or rerunning selection/research, preserving historical v1/v2 rankings.
func (m *MarketDB) LoadDashboardReport(ctx context.Context, asof string) (selection.Report, error) {
	if err := ctx.Err(); err != nil {
		return selection.Report{}, err
	}
	if m == nil || m.db == nil {
		return selection.Report{}, fs.ErrNotExist
	}
	return readDashboardReport(ctx, m.db, asof)
}

func readDashboardReport(ctx context.Context, reader sqlReader, asof string) (selection.Report, error) {
	var data []byte
	var report selection.Report
	err := reader.QueryRowContext(ctx, "SELECT report_json FROM selection_run WHERE as_of=?", asof).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return report, fs.ErrNotExist
	}
	if err != nil {
		return report, dashboardReadError(ctx)
	}
	if err := json.Unmarshal(data, &report); err != nil || (report.Version != 1 && report.Version != 2) || report.AsOf != asof ||
		(report.Adjust != "qfq" && report.Adjust != "hfq" && report.Adjust != "none") {
		return report, errors.New("invalid stored dashboard report")
	}
	return report, nil
}

func dashboardReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("cannot read dashboard snapshot")
}

// LoadDashboardHistory returns only the latest validated window for the configured
// adjustment. Do not splice historical qfq windows or fall back across isolation.
func (m *MarketDB) LoadDashboardHistory(ctx context.Context, stock config.Stock, adjust string) (market.History, error) {
	var empty market.History
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if m == nil || m.db == nil {
		return empty, fs.ErrNotExist
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, dashboardReadError(ctx)
	}
	defer tx.Rollback()
	var asof sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT MAX(as_of) FROM stock_sync WHERE adjust=?", adjust).Scan(&asof); err != nil {
		return empty, dashboardReadError(ctx)
	}
	if !asof.Valid {
		return empty, fs.ErrNotExist
	}
	if stock.Name == "" {
		if err := tx.QueryRowContext(ctx, "SELECT name FROM stock WHERE code=?", stock.Code).Scan(&stock.Name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return empty, fs.ErrNotExist
			}
			return empty, dashboardReadError(ctx)
		}
	}
	history, err := loadHistory(ctx, tx, stock, asof.String, adjust)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, fs.ErrNotExist
	}
	if err != nil {
		return empty, dashboardReadError(ctx)
	}
	if err := tx.Commit(); err != nil {
		return empty, dashboardReadError(ctx)
	}
	return history, nil
}
