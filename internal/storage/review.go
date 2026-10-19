package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

const reportArchiveIndexName = "idx_report_archive_as_of_generated_at"

// ReportRevision is the lightweight index entry for a persisted report revision.
type ReportRevision struct {
	ID            string    `json:"revision"`
	AsOf          string    `json:"as_of"`
	GeneratedAt   time.Time `json:"generated_at"`
	SchemaVersion int       `json:"schema_version"`
}

// initReviewSchema adds the report index columns and backfills them from the
// immutable payloads. Corrupt legacy payloads remain untouched and unindexed.
func (m *MarketDB) initReviewSchema(backfill bool) error {
	tx, err := m.db.Begin()
	if err != nil {
		return errors.New("cannot start review schema migration")
	}
	defer tx.Rollback()
	for _, column := range []struct {
		name       string
		definition string
	}{
		{"as_of", "TEXT NOT NULL DEFAULT ''"},
		{"generated_at", "TEXT NOT NULL DEFAULT ''"},
		{"schema_version", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(tx, "report_archive", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS ` + reportArchiveIndexName + ` ON report_archive(as_of, generated_at)`); err != nil {
		return errors.New("cannot create report archive index")
	}
	if backfill {
		if err := backfillReportArchive(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version=6"); err != nil {
		return errors.New("cannot update market database version")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit review schema migration")
	}
	return nil
}

func backfillReportArchive(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT id, report_json FROM report_archive
	`)
	if err != nil {
		return errors.New("cannot read report archive for backfill")
	}
	type update struct {
		id            string
		asOf          string
		generatedAt   string
		schemaVersion int
	}
	var updates []update
	for rows.Next() {
		var item update
		var data []byte
		var report selection.Report
		if err := rows.Scan(&item.id, &data); err != nil {
			rows.Close()
			return errors.New("cannot read report archive for backfill")
		}
		if err := json.Unmarshal(data, &report); err != nil {
			continue
		}
		item.asOf = report.AsOf
		if !report.GeneratedAt.IsZero() {
			item.generatedAt = report.GeneratedAt.UTC().Format(time.RFC3339Nano)
		}
		item.schemaVersion = report.Version
		updates = append(updates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("cannot read report archive for backfill")
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.Exec(`
			UPDATE report_archive SET as_of=?, generated_at=?, schema_version=? WHERE id=?
		`, item.asOf, item.generatedAt, item.schemaVersion, item.id); err != nil {
			return errors.New("cannot backfill report archive")
		}
	}
	return nil
}

// ListReportRevisions returns parseable archived reports for one trading date,
// ordered by generation time and revision ID. Invalid input yields an empty list.
func (m *MarketDB) ListReportRevisions(ctx context.Context, asof string) ([]ReportRevision, error) {
	out := []ReportRevision{}
	date, err := time.Parse("2006-01-02", asof)
	if err != nil || date.Format("2006-01-02") != asof || m == nil || m.db == nil {
		return out, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, as_of, generated_at, schema_version, report_json
		FROM report_archive WHERE as_of=? ORDER BY generated_at, id
	`, asof)
	if err != nil {
		return out, fmt.Errorf("cannot list report revisions: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var revision ReportRevision
		var generated string
		var data []byte
		var report selection.Report
		if err := rows.Scan(&revision.ID, &revision.AsOf, &generated, &revision.SchemaVersion, &data); err != nil {
			return out, fmt.Errorf("cannot list report revisions: %w", err)
		}
		revision.GeneratedAt, err = time.Parse(time.RFC3339Nano, generated)
		if err != nil || json.Unmarshal(data, &report) != nil || report.AsOf != asof || seen[revision.ID] {
			continue
		}
		seen[revision.ID] = true
		out = append(out, revision)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("cannot list report revisions: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GeneratedAt.Equal(out[j].GeneratedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].GeneratedAt.Before(out[j].GeneratedAt)
	})
	return out, nil
}

// LatestReviewDataDate returns the latest successful sync date across the
// requested codes and adjustment, or fs.ErrNotExist when none is available.
func (m *MarketDB) LatestReviewDataDate(ctx context.Context, codes []string, adjust string) (string, error) {
	if m == nil || m.db == nil {
		return "", fs.ErrNotExist
	}
	codes = uniqueReviewCodes(codes)
	if len(codes) == 0 {
		return "", fs.ErrNotExist
	}
	query := "SELECT MAX(as_of) FROM stock_sync WHERE adjust=? AND status='ok' AND code IN (" + placeholders(len(codes)) + ")"
	args := make([]any, 1, len(codes)+1)
	args[0] = adjust
	for _, code := range codes {
		args = append(args, code)
	}
	var latest sql.NullString
	if err := m.db.QueryRowContext(ctx, query, args...).Scan(&latest); err != nil {
		return "", fmt.Errorf("cannot read latest review data date: %w", err)
	}
	if !latest.Valid || latest.String == "" {
		return "", fs.ErrNotExist
	}
	return latest.String, nil
}

// LoadLatestReviewHistories returns each code's latest successful window at or
// before limitAsOf. Missing codes are skipped; other read errors are returned.
func (m *MarketDB) LoadLatestReviewHistories(ctx context.Context, codes []string, adjust, limitAsOf string) (map[string]market.History, error) {
	out := map[string]market.History{}
	if m == nil || m.db == nil {
		return out, nil
	}
	codes = uniqueReviewCodes(codes)
	if len(codes) == 0 {
		return out, nil
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("cannot begin review read transaction: %w", err)
	}
	defer tx.Rollback()
	for _, code := range codes {
		var asof, name string
		err := tx.QueryRowContext(ctx, `
			SELECT sync.as_of, stock.name
			FROM stock_sync AS sync JOIN stock ON stock.code=sync.code
			WHERE sync.code=? AND sync.adjust=? AND sync.status='ok' AND sync.as_of<=?
			ORDER BY sync.as_of DESC LIMIT 1
		`, code, adjust, limitAsOf).Scan(&asof, &name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("cannot read latest review sync for %s: %w", code, err)
		}
		history, err := loadHistory(ctx, tx, config.Stock{Code: code, Name: name}, asof, adjust)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("cannot load latest review history for %s: %w", code, err)
		}
		out[code] = history
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("cannot commit review read transaction: %w", err)
	}
	return out, nil
}

func uniqueReviewCodes(codes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func placeholders(count int) string {
	out := make([]byte, 0, count*2-1)
	for i := 0; i < count; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
