package predictor

import (
	"context"
	"io/fs"
	"time"

	calpkg "github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type reportArchiveReader interface {
	ListReportRevisions(context.Context, string) ([]storage.ReportRevision, error)
	LoadReportRevision(context.Context, string) (selection.Report, error)
	LoadReport(context.Context, string) (selection.Report, error)
}

// LoadPointInTimeReport returns the latest report for target that was actually
// generated before the target session opened. Archived revisions are preferred;
// the mutable selection_run table is a legacy fallback only.
//
// allowLateArchive relaxes only the generation timestamp: an archived report
// still must have as_of strictly before target. This supports delayed report
// generation without allowing target-session data into the universe.
func LoadPointInTimeReport(ctx context.Context, reader reportArchiveReader, target string, cal *calpkg.Calendar, allowLateArchive bool) (selection.Report, string, error) {
	if reader == nil || cal == nil {
		return selection.Report{}, "", fs.ErrNotExist
	}
	day, err := time.ParseInLocation("2006-01-02", target, market.Shanghai)
	if err != nil {
		return selection.Report{}, "", err
	}
	cutoff := time.Date(day.Year(), day.Month(), day.Day(), 9, 30, 0, 0, market.Shanghai)
	var best selection.Report
	bestSource := ""
	var late selection.Report
	for i := 1; i <= 15; i++ {
		asof := calpkg.Date(day.AddDate(0, 0, -i))
		revisions, err := reader.ListReportRevisions(ctx, asof)
		if err != nil {
			return selection.Report{}, "", err
		}
		for _, revision := range revisions {
			report, err := reader.LoadReportRevision(ctx, revision.ID)
			if err != nil || report.Target != target {
				continue
			}
			if report.AsOf == "" || report.AsOf >= target {
				continue
			}
			if report.GeneratedAt.IsZero() {
				report.GeneratedAt = revision.GeneratedAt
			}
			if report.GeneratedAt.IsZero() {
				continue
			}
			if report.GeneratedAt.Before(cutoff) {
				if best.ID == "" || report.AsOf > best.AsOf || (report.AsOf == best.AsOf && report.GeneratedAt.After(best.GeneratedAt)) {
					best = report
					bestSource = "archive"
				}
				continue
			}
			if allowLateArchive {
				if late.ID == "" || report.AsOf > late.AsOf || (report.AsOf == late.AsOf && report.GeneratedAt.After(late.GeneratedAt)) {
					late = report
				}
			}
		}
	}
	if best.ID != "" {
		return best, bestSource, nil
	}
	if late.ID != "" {
		return late, "archive-late", nil
	}
	var lateSelection selection.Report
	for i := 1; i <= 15; i++ {
		asof := calpkg.Date(day.AddDate(0, 0, -i))
		report, err := reader.LoadReport(ctx, asof)
		if err != nil || report.Target != target {
			continue
		}
		if report.AsOf == "" || report.AsOf >= target {
			continue
		}
		if report.GeneratedAt.IsZero() {
			continue
		}
		if !report.GeneratedAt.Before(cutoff) {
			if allowLateArchive &&
				(lateSelection.ID == "" || report.AsOf > lateSelection.AsOf || (report.AsOf == lateSelection.AsOf && report.GeneratedAt.After(lateSelection.GeneratedAt))) {
				lateSelection = report
			}
			continue
		}
		if best.ID == "" || report.AsOf > best.AsOf || (report.AsOf == best.AsOf && report.GeneratedAt.After(best.GeneratedAt)) {
			best = report
			bestSource = "selection_run"
		}
	}
	if best.ID == "" {
		if lateSelection.ID != "" {
			return lateSelection, "selection_run-late", nil
		}
		return selection.Report{}, "", fs.ErrNotExist
	}
	return best, bestSource, nil
}
