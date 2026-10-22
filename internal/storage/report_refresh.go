package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

// ReplaceReport publishes a derived report revision while keeping the frozen
// market input and every previously archived revision unchanged.
func (m *MarketDB) ReplaceReport(ctx context.Context, report selection.Report) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if report.AsOf == "" || report.InputID == "" {
		return errors.New("derived report requires as_of and input_id")
	}
	report.Revision = ""
	raw, err := json.Marshal(report)
	if err != nil {
		return errors.New("cannot encode derived selection report")
	}
	report.Revision = sha256Sum(raw)
	raw, err = json.Marshal(report)
	if err != nil {
		return errors.New("cannot encode derived selection report")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin derived report transaction")
	}
	defer tx.Rollback()
	var currentRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT report_json FROM selection_run WHERE as_of=?`, report.AsOf).Scan(&currentRaw); err != nil {
		return errors.New("cannot load the report being refreshed")
	}
	var current selection.Report
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		return errors.New("stored report is invalid")
	}
	if current.AsOf != report.AsOf || current.Target != report.Target || current.InputID == "" || current.InputID != report.InputID {
		return errors.New("derived report does not match the frozen input")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO report_archive(id, report_json, as_of, generated_at, schema_version)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING
	`, report.Revision, raw, report.AsOf, report.GeneratedAt.UTC().Format(time.RFC3339Nano), report.Version); err != nil {
		return errors.New("cannot archive derived selection report")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO selection_run(as_of, id, generated_at, report_json)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(as_of) DO UPDATE SET id=excluded.id, generated_at=excluded.generated_at, report_json=excluded.report_json
	`, report.AsOf, report.ID, report.GeneratedAt.UTC().Format(time.RFC3339Nano), raw); err != nil {
		return errors.New("cannot persist derived selection report")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM selection_result WHERE as_of=?", report.AsOf); err != nil {
		return errors.New("cannot replace derived selection results")
	}
	if err := insertSelectionResults(ctx, tx, report); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit derived selection report")
	}
	return nil
}
