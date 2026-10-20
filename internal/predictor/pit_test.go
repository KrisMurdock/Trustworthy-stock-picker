package predictor

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type fakePITReader struct {
	revisions map[string][]storage.ReportRevision
	archives  map[string]selection.Report
	current   map[string]selection.Report
}

func (f *fakePITReader) ListReportRevisions(_ context.Context, asof string) ([]storage.ReportRevision, error) {
	return f.revisions[asof], nil
}

func (f *fakePITReader) LoadReportRevision(_ context.Context, id string) (selection.Report, error) {
	report, ok := f.archives[id]
	if !ok {
		return selection.Report{}, fs.ErrNotExist
	}
	return report, nil
}

func (f *fakePITReader) LoadReport(_ context.Context, asof string) (selection.Report, error) {
	report, ok := f.current[asof]
	if !ok {
		return selection.Report{}, fs.ErrNotExist
	}
	return report, nil
}

func pitCalendar() *calendar.Calendar {
	c := config.Defaults()
	c.Calendar.ValidFrom = "2026-09-01"
	c.Calendar.ValidThrough = "2026-12-31"
	return calendar.New(c)
}

func pitReport(id, asof, target string, generated time.Time) selection.Report {
	return selection.Report{ID: id, AsOf: asof, Target: target, GeneratedAt: generated}
}

func TestLoadPointInTimeReportPrefersArchiveBeforeOpen(t *testing.T) {
	generated := time.Date(2026, 9, 2, 18, 5, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{
			"2026-09-02": {
				{ID: "good", AsOf: "2026-09-02", GeneratedAt: generated},
				{ID: "late", AsOf: "2026-09-02", GeneratedAt: time.Date(2026, 9, 3, 10, 0, 0, 0, generated.Location())},
			},
		},
		archives: map[string]selection.Report{
			"good": pitReport("good", "2026-09-02", "2026-09-03", generated),
			"late": pitReport("late", "2026-09-02", "2026-09-03", time.Date(2026, 9, 3, 10, 0, 0, 0, generated.Location())),
		},
		current: map[string]selection.Report{},
	}
	report, source, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false)
	if err != nil {
		t.Fatalf("load report: %v", err)
	}
	if report.ID != "good" || source != "archive" {
		t.Fatalf("report=%+v source=%q", report, source)
	}
}

func TestLoadPointInTimeReportFallsBackToCurrentReport(t *testing.T) {
	generated := time.Date(2026, 9, 2, 18, 5, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{},
		archives:  map[string]selection.Report{},
		current: map[string]selection.Report{
			"2026-09-02": pitReport("current", "2026-09-02", "2026-09-03", generated),
		},
	}
	report, source, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false)
	if err != nil {
		t.Fatalf("load report: %v", err)
	}
	if report.ID != "current" || source != "selection_run" {
		t.Fatalf("report=%+v source=%q", report, source)
	}
}

func TestLoadPointInTimeReportRejectsPostOpenReport(t *testing.T) {
	generated := time.Date(2026, 9, 3, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{},
		archives:  map[string]selection.Report{},
		current: map[string]selection.Report{
			"2026-09-02": pitReport("late", "2026-09-02", "2026-09-03", generated),
		},
	}
	_, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("post-open report error = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadPointInTimeReportRejectsSameDayAsOf(t *testing.T) {
	generated := time.Date(2026, 9, 3, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{
			"2026-09-02": {{ID: "same-asof", AsOf: "2026-09-02", GeneratedAt: generated}},
		},
		archives: map[string]selection.Report{
			"same-asof": pitReport("same-asof", "2026-09-03", "2026-09-03", generated),
		},
		current: map[string]selection.Report{
			"2026-09-02": pitReport("same-current", "2026-09-03", "2026-09-03", generated),
		},
	}
	if _, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("archive with same-day as_of accepted: %v", err)
	}
	reader.revisions = map[string][]storage.ReportRevision{}
	reader.archives = map[string]selection.Report{}
	if _, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("selection_run with same-day as_of accepted: %v", err)
	}
}

func TestLoadPointInTimeReportRejectsUnknownGeneratedAt(t *testing.T) {
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{},
		archives:  map[string]selection.Report{},
		current: map[string]selection.Report{
			"2026-09-02": pitReport("unknown-time", "2026-09-02", "2026-09-03", time.Time{}),
		},
	}
	if _, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("report without generated_at accepted: %v", err)
	}
}

func TestLoadPointInTimeReportLateArchiveRequiresPriorAsOf(t *testing.T) {
	generated := time.Date(2026, 9, 3, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{
			"2026-09-02": {
				{ID: "late-prior", AsOf: "2026-09-02", GeneratedAt: generated},
				{ID: "late-same", AsOf: "2026-09-03", GeneratedAt: generated},
			},
		},
		archives: map[string]selection.Report{
			"late-prior": pitReport("late-prior", "2026-09-02", "2026-09-03", generated),
			"late-same":  pitReport("late-same", "2026-09-03", "2026-09-03", generated),
		},
		current: map[string]selection.Report{},
	}
	report, source, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), true)
	if err != nil {
		t.Fatalf("load late archive: %v", err)
	}
	if report.ID != "late-prior" || source != "archive-late" {
		t.Fatalf("report=%+v source=%q", report, source)
	}
	_, _, err = LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), false)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("strict PIT accepted late archive: %v", err)
	}

	reader.revisions = map[string][]storage.ReportRevision{
		"2026-09-02": {{ID: "late-same", AsOf: "2026-09-03", GeneratedAt: generated}},
	}
	if _, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("late archive with same-day as_of accepted: %v", err)
	}
}

func TestLoadPointInTimeReportLateSelectionRunRequiresPriorAsOf(t *testing.T) {
	generated := time.Date(2026, 9, 3, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	reader := &fakePITReader{
		revisions: map[string][]storage.ReportRevision{},
		archives:  map[string]selection.Report{},
		current: map[string]selection.Report{
			"2026-09-02": pitReport("late-current", "2026-09-02", "2026-09-03", generated),
		},
	}
	report, source, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), true)
	if err != nil {
		t.Fatalf("load late selection run: %v", err)
	}
	if report.ID != "late-current" || source != "selection_run-late" {
		t.Fatalf("report=%+v source=%q", report, source)
	}
	reader.current["2026-09-02"] = pitReport("same-day", "2026-09-03", "2026-09-03", generated)
	if _, _, err := LoadPointInTimeReport(context.Background(), reader, "2026-09-03", pitCalendar(), true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("late selection run with same-day as_of accepted: %v", err)
	}
}
