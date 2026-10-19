package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type reviewLoader struct {
	config config.Config
	db     *storage.MarketDB
}

func newReviewLoader(c config.Config, db *storage.MarketDB) *reviewLoader {
	return &reviewLoader{config: c, db: db}
}

func (l *reviewLoader) ListRevisions(ctx context.Context, asof string) ([]review.Revision, error) {
	items, err := l.db.ListReportRevisions(ctx, asof)
	if err != nil {
		return nil, err
	}
	out := make([]review.Revision, 0, len(items))
	for _, item := range items {
		out = append(out, review.Revision{ID: item.ID, AsOf: item.AsOf, GeneratedAt: item.GeneratedAt, SchemaVersion: item.SchemaVersion})
	}
	return out, nil
}

func (l *reviewLoader) LoadReport(ctx context.Context, asof, revision string) (selection.Report, error) {
	if revision != "" {
		return l.db.LoadReportRevision(ctx, revision)
	}
	return l.db.LoadDashboardReport(ctx, asof)
}

func (l *reviewLoader) LoadInputs(ctx context.Context, report selection.Report) (map[string]market.History, error) {
	if report.InputID == "" {
		return nil, fs.ErrNotExist
	}
	items, err := l.db.LoadReportInputs(ctx, report.InputID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]market.History, len(items))
	for _, item := range items {
		if item.Raw != nil {
			out[item.Stock.Code] = *item.Raw
		}
	}
	return out, nil
}

func (l *reviewLoader) LoadLatestMarkets(ctx context.Context, codes []string) (map[string]market.History, string, error) {
	latest, err := l.db.LatestReviewDataDate(ctx, codes, "none")
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]market.History{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	histories, err := l.db.LoadLatestReviewHistories(ctx, codes, "none", latest)
	if err != nil {
		return nil, "", err
	}
	return histories, latest, nil
}

func (l *reviewLoader) LoadCalendar(ctx context.Context, from, through string) (review.CalendarWindow, error) {
	if through <= from {
		return review.CalendarWindow{}, fs.ErrNotExist
	}
	if cache, err := storage.OpenCalendarView(l.config.DataDir, l.config.RequireCIFS); err == nil {
		defer cache.Close()
		if history, freezeErr := cache.Freeze(ctx, from, through); freezeErr == nil {
			return review.CalendarWindow{From: from, Through: through, Days: append([]string(nil), history.Days...), Source: history.Source}, nil
		} else if !errors.Is(freezeErr, fs.ErrNotExist) && !isCalendarCoverage(freezeErr) {
			return review.CalendarWindow{}, freezeErr
		}
	}
	days, err := calendar.New(l.config).TradingDays(from, through)
	if err != nil {
		return review.CalendarWindow{}, err
	}
	source := l.config.Calendar.Source
	if strings.TrimSpace(source) == "" {
		source = "configured"
	}
	return review.CalendarWindow{From: from, Through: through, Days: days, Source: source}, nil
}

func isCalendarCoverage(err error) bool {
	var coverage *calendar.CoverageError
	return errors.As(err, &coverage)
}
