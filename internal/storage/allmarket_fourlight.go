package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const allMarketFourLightSchema = `
	CREATE TABLE IF NOT EXISTS four_light_run (
		id TEXT PRIMARY KEY,
		as_of TEXT NOT NULL,
		rule TEXT NOT NULL,
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('running', 'complete', 'partial', 'failed')),
		total INTEGER NOT NULL DEFAULT 0 CHECK (total >= 0),
		ready INTEGER NOT NULL DEFAULT 0 CHECK (ready >= 0),
		all_red INTEGER NOT NULL DEFAULT 0 CHECK (all_red >= 0),
		first_all_red INTEGER NOT NULL DEFAULT 0 CHECK (first_all_red >= 0),
		continuing_all_red INTEGER NOT NULL DEFAULT 0 CHECK (continuing_all_red >= 0),
		exit_all_red INTEGER NOT NULL DEFAULT 0 CHECK (exit_all_red >= 0),
		non_all_red INTEGER NOT NULL DEFAULT 0 CHECK (non_all_red >= 0),
		unknown INTEGER NOT NULL DEFAULT 0 CHECK (unknown >= 0),
		error_kind TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS four_light_daily (
		as_of TEXT NOT NULL,
		rule TEXT NOT NULL,
		code TEXT NOT NULL REFERENCES stock(code),
		name TEXT NOT NULL DEFAULT '',
		previous_trade_date TEXT NOT NULL DEFAULT '',
		data_status TEXT NOT NULL CHECK (data_status IN ('ready', 'partial', 'unknown')),
		state TEXT NOT NULL CHECK (state IN ('all_red', 'non_all_red', 'unknown')),
		lifecycle TEXT NOT NULL CHECK (lifecycle IN ('first_all_red', 'continuing_all_red', 'exit_all_red', 'non_all_red', 'unknown')),
		trend TEXT NOT NULL CHECK (trend IN ('red', 'green', 'unknown')),
		energy TEXT NOT NULL CHECK (energy IN ('red', 'green', 'unknown')),
		mid_term TEXT NOT NULL CHECK (mid_term IN ('red', 'green', 'unknown')),
		short_term TEXT NOT NULL CHECK (short_term IN ('red', 'green', 'unknown')),
		unknown_lights INTEGER NOT NULL DEFAULT 0 CHECK (unknown_lights >= 0),
		bar_count INTEGER NOT NULL DEFAULT 0 CHECK (bar_count >= 0),
		latest_bar_date TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		data_field TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		computed_at TEXT NOT NULL,
		run_id TEXT NOT NULL REFERENCES four_light_run(id),
		PRIMARY KEY (as_of, rule, code)
	);
	CREATE TABLE IF NOT EXISTS four_light_event (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		as_of TEXT NOT NULL,
		rule TEXT NOT NULL,
		code TEXT NOT NULL REFERENCES stock(code),
		name TEXT NOT NULL DEFAULT '',
		event TEXT NOT NULL CHECK (event IN ('first_all_red', 'exit_all_red')),
		previous_trade_date TEXT NOT NULL DEFAULT '',
		lights_json TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		computed_at TEXT NOT NULL,
		run_id TEXT NOT NULL REFERENCES four_light_run(id),
		UNIQUE (as_of, rule, code, event)
	);
	CREATE INDEX IF NOT EXISTS four_light_daily_lifecycle ON four_light_daily(as_of, rule, lifecycle, code);
	CREATE INDEX IF NOT EXISTS four_light_daily_code ON four_light_daily(code, rule, as_of);
	CREATE INDEX IF NOT EXISTS four_light_event_as_of ON four_light_event(as_of, rule, event, code);
`

type AllMarketBarWindow struct {
	Code      string       `json:"code"`
	Adjust    string       `json:"adjust"`
	Bars      []market.Bar `json:"bars"`
	Source    string       `json:"source,omitempty"`
	DataField string       `json:"data_field,omitempty"`
}

type FourLightRun struct {
	ID               string    `json:"id"`
	AsOf             string    `json:"as_of"`
	Rule             string    `json:"rule"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Status           string    `json:"status"`
	Total            int       `json:"total"`
	Ready            int       `json:"ready"`
	AllRed           int       `json:"all_red"`
	FirstAllRed      int       `json:"first_all_red"`
	ContinuingAllRed int       `json:"continuing_all_red"`
	ExitAllRed       int       `json:"exit_all_red"`
	NonAllRed        int       `json:"non_all_red"`
	Unknown          int       `json:"unknown"`
	ErrorKind        string    `json:"error_kind,omitempty"`
}

type FourLightRecord struct {
	AsOf              string    `json:"as_of"`
	Rule              string    `json:"rule"`
	Code              string    `json:"code"`
	Name              string    `json:"name"`
	PreviousTradeDate string    `json:"previous_trade_date,omitempty"`
	DataStatus        string    `json:"data_status"`
	State             string    `json:"state"`
	Lifecycle         string    `json:"lifecycle"`
	Trend             string    `json:"trend"`
	Energy            string    `json:"energy"`
	MidTerm           string    `json:"mid_term"`
	ShortTerm         string    `json:"short_term"`
	UnknownLights     int       `json:"unknown_lights"`
	BarCount          int       `json:"bar_count"`
	LatestBarDate     string    `json:"latest_bar_date,omitempty"`
	Source            string    `json:"source,omitempty"`
	DataField         string    `json:"data_field,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	ComputedAt        time.Time `json:"computed_at"`
	RunID             string    `json:"run_id"`
}

type FourLightSummary struct {
	Run        FourLightRun      `json:"run"`
	First      []FourLightRecord `json:"first_all_red"`
	Continuing []FourLightRecord `json:"continuing_all_red"`
	Exited     []FourLightRecord `json:"exit_all_red"`
	Unknown    []FourLightRecord `json:"unknown_records"`
}

const fourLightStatusLimit = 200

func (m *AllMarketDB) ActiveStocks(ctx context.Context) ([]config.Stock, error) {
	if m == nil || m.db == nil {
		return nil, fs.ErrNotExist
	}
	rows, err := m.db.QueryContext(ctx, "SELECT code, name, listed_from FROM stock WHERE active=1 ORDER BY code")
	if err != nil {
		return nil, errors.New("cannot read all-market stock universe")
	}
	defer rows.Close()
	out := []config.Stock{}
	for rows.Next() {
		var stock config.Stock
		if err := rows.Scan(&stock.Code, &stock.Name, &stock.ListedFrom); err != nil {
			return nil, errors.New("cannot decode all-market stock universe")
		}
		out = append(out, stock)
	}
	return out, rows.Err()
}

func (m *AllMarketDB) LoadBarsThrough(ctx context.Context, code, adjust, through string, limit int) (AllMarketBarWindow, error) {
	window := AllMarketBarWindow{Code: code, Adjust: adjust}
	if m == nil || m.db == nil {
		return window, fs.ErrNotExist
	}
	if !config.ValidCode(code) || (adjust != "none" && adjust != "qfq") || through == "" || limit <= 0 || limit > 5000 {
		return window, errors.New("invalid all-market bar window")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT trade_date, open_micros, close_micros, high_micros, low_micros, volume_raw, source, data_field
		FROM daily_bar WHERE code=? AND adjust=? AND trade_date<=?
		ORDER BY trade_date DESC LIMIT ?
	`, code, adjust, through, limit)
	if err != nil {
		return window, errors.New("cannot read all-market bar window")
	}
	defer rows.Close()
	for rows.Next() {
		var date, source, dataField string
		var open, close, high, low int64
		var volume float64
		if err := rows.Scan(&date, &open, &close, &high, &low, &volume, &source, &dataField); err != nil {
			return window, errors.New("cannot decode all-market bar window")
		}
		window.Bars = append(window.Bars, market.Bar{Date: date, Open: float64(open) / 1_000_000, Close: float64(close) / 1_000_000,
			High: float64(high) / 1_000_000, Low: float64(low) / 1_000_000, Volume: volume})
		if len(window.Bars) == 1 {
			window.Source, window.DataField = source, dataField
		}
	}
	if err := rows.Err(); err != nil {
		return window, err
	}
	if len(window.Bars) == 0 {
		return window, fs.ErrNotExist
	}
	for left, right := 0, len(window.Bars)-1; left < right; left, right = left+1, right-1 {
		window.Bars[left], window.Bars[right] = window.Bars[right], window.Bars[left]
	}
	return window, nil
}

func (m *AllMarketDB) StartFourLightRun(ctx context.Context, run FourLightRun) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if err := validateFourLightRun(run, "running"); err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE four_light_run SET status='failed', finished_at=?, error_kind='stale' WHERE status='running'
	`, run.StartedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot clean stale four-light runs")
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO four_light_run(id, as_of, rule, started_at, finished_at, status, total)
		VALUES(?, ?, ?, ?, ?, 'running', ?)
	`, run.ID, run.AsOf, run.Rule, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Total)
	if err != nil {
		return errors.New("cannot start four-light run")
	}
	return nil
}

func (m *AllMarketDB) CompleteFourLightRun(ctx context.Context, run FourLightRun, records []FourLightRecord) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if err := validateFourLightRun(run, run.Status); err != nil {
		return err
	}
	if run.Status != "complete" && run.Status != "partial" {
		return errors.New("invalid completed four-light run status")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin four-light completion transaction")
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE four_light_run SET finished_at=?, status=?, total=?, ready=?, all_red=?, first_all_red=?,
			continuing_all_red=?, exit_all_red=?, non_all_red=?, unknown=?, error_kind=? WHERE id=?
	`, run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Status, run.Total, run.Ready, run.AllRed, run.FirstAllRed,
		run.ContinuingAllRed, run.ExitAllRed, run.NonAllRed, run.Unknown, run.ErrorKind, run.ID)
	if err != nil {
		return rollback(errors.New("cannot finish four-light run"))
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return rollback(errors.New("four-light run is not available"))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM four_light_event WHERE as_of=? AND rule=?", run.AsOf, run.Rule); err != nil {
		return rollback(errors.New("cannot replace four-light events"))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM four_light_daily WHERE as_of=? AND rule=?", run.AsOf, run.Rule); err != nil {
		return rollback(errors.New("cannot replace four-light daily records"))
	}
	for _, record := range records {
		if record.AsOf != run.AsOf || record.Rule != run.Rule || record.RunID != run.ID || !config.ValidCode(record.Code) {
			return rollback(errors.New("invalid four-light daily record"))
		}
		computed := record.ComputedAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO four_light_daily(as_of, rule, code, name, previous_trade_date, data_status, state, lifecycle,
				trend, energy, mid_term, short_term, unknown_lights, bar_count, latest_bar_date, source, data_field,
				reason, computed_at, run_id)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, record.AsOf, record.Rule, record.Code, record.Name, record.PreviousTradeDate, record.DataStatus, record.State,
			record.Lifecycle, record.Trend, record.Energy, record.MidTerm, record.ShortTerm, record.UnknownLights,
			record.BarCount, record.LatestBarDate, record.Source, record.DataField, record.Reason, computed, record.RunID); err != nil {
			return rollback(errors.New("cannot persist four-light daily record"))
		}
		if record.Lifecycle != "first_all_red" && record.Lifecycle != "exit_all_red" {
			continue
		}
		lights := `{"trend":"` + record.Trend + `","energy":"` + record.Energy + `","mid_term":"` + record.MidTerm + `","short_term":"` + record.ShortTerm + `"}`
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO four_light_event(as_of, rule, code, name, event, previous_trade_date, lights_json, reason, computed_at, run_id)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(as_of, rule, code, event) DO UPDATE SET name=excluded.name,
				previous_trade_date=excluded.previous_trade_date, lights_json=excluded.lights_json,
				reason=excluded.reason, computed_at=excluded.computed_at, run_id=excluded.run_id
		`, record.AsOf, record.Rule, record.Code, record.Name, record.Lifecycle, record.PreviousTradeDate, lights,
			record.Reason, computed, record.RunID); err != nil {
			return rollback(errors.New("cannot persist four-light event"))
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit four-light completion")
	}
	return nil
}

func (m *AllMarketDB) FailFourLightRun(ctx context.Context, run FourLightRun) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if run.ID == "" || run.Rule == "" || run.AsOf == "" {
		return errors.New("invalid four-light run")
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE four_light_run SET finished_at=?, status='failed', total=?, error_kind=? WHERE id=?
	`, run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Total, run.ErrorKind, run.ID)
	if err != nil {
		return errors.New("cannot fail four-light run")
	}
	return nil
}

func (m *AllMarketDB) LatestFourLightSummary(ctx context.Context, asOf, rule string) (FourLightSummary, error) {
	var summary FourLightSummary
	if m == nil || m.db == nil {
		return summary, fs.ErrNotExist
	}
	if rule == "" {
		return summary, errors.New("four-light rule is required")
	}
	query := `
		SELECT id, as_of, rule, started_at, finished_at, status, total, ready, all_red, first_all_red,
			continuing_all_red, exit_all_red, non_all_red, unknown, error_kind
		FROM four_light_run WHERE rule=? AND status IN ('complete', 'partial')`
	args := []any{rule}
	if asOf != "" {
		query += " AND as_of=?"
		args = append(args, asOf)
	}
	query += " ORDER BY as_of DESC, finished_at DESC LIMIT 1"
	var started, finished string
	err := m.db.QueryRowContext(ctx, query, args...).Scan(&summary.Run.ID, &summary.Run.AsOf, &summary.Run.Rule,
		&started, &finished, &summary.Run.Status, &summary.Run.Total, &summary.Run.Ready, &summary.Run.AllRed,
		&summary.Run.FirstAllRed, &summary.Run.ContinuingAllRed, &summary.Run.ExitAllRed, &summary.Run.NonAllRed,
		&summary.Run.Unknown, &summary.Run.ErrorKind)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, fs.ErrNotExist
	}
	if err != nil {
		return summary, errors.New("cannot read four-light run")
	}
	summary.Run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	summary.Run.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	for lifecycle, target := range map[string]*[]FourLightRecord{
		"first_all_red":      &summary.First,
		"continuing_all_red": &summary.Continuing,
		"exit_all_red":       &summary.Exited,
		"unknown":            &summary.Unknown,
	} {
		records, err := m.fourLightRecords(ctx, summary.Run.AsOf, rule, lifecycle, fourLightStatusLimit)
		if err != nil {
			return summary, err
		}
		*target = records
	}
	return summary, nil
}

func (m *AllMarketDB) FourLightHistory(ctx context.Context, code, rule, from, through string) ([]FourLightRecord, error) {
	if m == nil || m.db == nil {
		return nil, fs.ErrNotExist
	}
	if !config.ValidCode(code) || rule == "" || from == "" || through == "" || from > through {
		return nil, errors.New("invalid four-light history query")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT as_of, rule, code, name, previous_trade_date, data_status, state, lifecycle, trend, energy,
			mid_term, short_term, unknown_lights, bar_count, latest_bar_date, source, data_field, reason,
			computed_at, run_id
		FROM four_light_daily WHERE code=? AND rule=? AND as_of>=? AND as_of<=? ORDER BY as_of
	`, code, rule, from, through)
	if err != nil {
		return nil, errors.New("cannot read four-light history")
	}
	defer rows.Close()
	out := []FourLightRecord{}
	for rows.Next() {
		record, err := scanFourLightRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, fs.ErrNotExist
	}
	return out, nil
}

func (m *AllMarketDB) fourLightRecords(ctx context.Context, asOf, rule, lifecycle string, limit int) ([]FourLightRecord, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT as_of, rule, code, name, previous_trade_date, data_status, state, lifecycle, trend, energy,
			mid_term, short_term, unknown_lights, bar_count, latest_bar_date, source, data_field, reason,
			computed_at, run_id
		FROM four_light_daily WHERE as_of=? AND rule=? AND lifecycle=? ORDER BY code LIMIT ?
	`, asOf, rule, lifecycle, limit)
	if err != nil {
		return nil, errors.New("cannot read four-light daily records")
	}
	defer rows.Close()
	out := []FourLightRecord{}
	for rows.Next() {
		record, err := scanFourLightRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanFourLightRecord(scanner rowScanner) (FourLightRecord, error) {
	var record FourLightRecord
	var computed string
	if err := scanner.Scan(&record.AsOf, &record.Rule, &record.Code, &record.Name, &record.PreviousTradeDate,
		&record.DataStatus, &record.State, &record.Lifecycle, &record.Trend, &record.Energy, &record.MidTerm,
		&record.ShortTerm, &record.UnknownLights, &record.BarCount, &record.LatestBarDate, &record.Source,
		&record.DataField, &record.Reason, &computed, &record.RunID); err != nil {
		return record, errors.New("cannot decode four-light daily record")
	}
	record.ComputedAt, _ = time.Parse(time.RFC3339Nano, computed)
	return record, nil
}

func validateFourLightRun(run FourLightRun, expected string) error {
	if run.ID == "" || run.AsOf == "" || run.Rule == "" || run.Status != expected {
		return errors.New("invalid four-light run")
	}
	if _, err := time.Parse("2006-01-02", run.AsOf); err != nil {
		return errors.New("invalid four-light run date")
	}
	if run.StartedAt.IsZero() || run.FinishedAt.IsZero() {
		return errors.New("invalid four-light run time")
	}
	return nil
}
