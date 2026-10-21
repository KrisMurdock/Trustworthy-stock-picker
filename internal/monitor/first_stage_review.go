package monitor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func (m *Monitor) enqueueFirstStageReview(ctx context.Context, state *State, evaluationDate string) (bool, error) {
	if !m.Config.FirstStage.Enabled || !m.Config.FirstStage.PushEnabled {
		return false, nil
	}
	signalDate, horizonDates, err := firstStageReviewWindow(m.Calendar, evaluationDate)
	if err != nil {
		return false, err
	}
	previous, err := m.readReport(ctx, signalDate)
	if errors.Is(err, fs.ErrNotExist) {
		m.Log.Debug("First-stage review skipped", "signal", signalDate, "evaluation", evaluationDate, "reason", "previous_report_missing")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previous.Target != horizonDates[0] {
		m.Log.Info("First-stage review skipped", "signal", signalDate, "evaluation", evaluationDate, "reason", "target_date_mismatch")
		return false, nil
	}
	result := previous.FirstStage
	if result == nil || !result.PushEnabled || result.Status != firststage.StatusOK || len(result.Candidates) == 0 {
		m.Log.Debug("First-stage review skipped", "signal", signalDate, "evaluation", evaluationDate, "reason", "previous_first_stage_unavailable")
		return false, nil
	}
	channels := m.channelNamesForRoute(config.NotifyRouteFirstStage)
	if len(channels) == 0 {
		m.Log.Debug("First-stage review skipped", "signal", signalDate, "evaluation", evaluationDate, "reason", "no_first_stage_channel")
		return false, nil
	}
	eventID := fmt.Sprintf("first-stage-review:%s:%s", signalDate, evaluationDate)
	if _, exists := state.Events[eventID]; exists {
		return false, nil
	}
	if m.Market == nil || !m.Market.HasDatabase() {
		return false, errors.New("market database is unavailable for first-stage review")
	}
	pool, err := firstStageReviewPool(result)
	if err != nil {
		return false, err
	}
	histories, err := m.Market.LoadHistories(ctx, pool, evaluationDate, m.Config.Daily.Adjust)
	if err != nil {
		return false, err
	}
	review, err := firststage.EvaluateHorizon(*result, histories, pool, firststage.ReviewOptions{
		SignalDate:     signalDate,
		EvaluationDate: evaluationDate,
		HorizonDates:   horizonDates,
		Adjust:         m.Config.Daily.Adjust,
		SelectedLimit:  firststage.PushLimit,
		ThresholdPct:   firststage.TouchThresholdPct,
		Now:            m.Now(),
	})
	if err != nil {
		return false, err
	}
	if review.Status == firststage.ReviewStatusUnavailable {
		m.Log.Warn("First-stage review has no complete evaluation data", "signal", signalDate, "evaluation", evaluationDate, "reason", review.Reason)
		return false, nil
	}
	message, err := reportmessage.FirstStageReviewCard(review)
	if err != nil {
		return false, err
	}
	if err := state.EnqueuePart(evaluationDate, message, channels, "first_stage_review", 1000, ""); err != nil {
		return false, err
	}
	if err := m.writeState(*state); err != nil {
		return false, err
	}
	m.Log.Info("First-stage review queued", "signal", signalDate, "window_start", horizonDates[0], "evaluation", evaluationDate, "hits", review.Hits, "evaluated", review.Evaluated, "pool_hits", review.PoolHits, "recall_pct", review.RecallPct)
	return true, nil
}

func firstStageReviewWindow(cal *calendar.Calendar, evaluationDate string) (string, []string, error) {
	t, err := time.ParseInLocation("2006-01-02", evaluationDate, market.Shanghai)
	if err != nil || t.Format("2006-01-02") != evaluationDate {
		return "", nil, errors.New("invalid first-stage evaluation date")
	}
	dates := make([]string, 0, firststage.HorizonDays)
	for i := 0; len(dates) < firststage.HorizonDays; i++ {
		if i > 31 {
			return "", nil, errors.New("cannot build first-stage review window")
		}
		candidate := calendar.Date(t)
		trading, err := cal.Trading(candidate)
		if err != nil {
			return "", nil, err
		}
		if trading {
			dates = append([]string{candidate}, dates...)
		}
		t = t.AddDate(0, 0, -1)
	}
	if dates[len(dates)-1] != evaluationDate {
		return "", nil, errors.New("first-stage evaluation date is not a trading day")
	}
	signalDate, err := previousTradingDay(cal, dates[0])
	if err != nil {
		return "", nil, err
	}
	return signalDate, dates, nil
}

func firstStageReviewPool(report *firststage.Report) ([]config.Stock, error) {
	pool := make([]config.Stock, 0, report.PoolSize)
	seen := make(map[string]bool, report.PoolSize)
	add := func(stock config.Stock) {
		if seen[stock.Code] {
			return
		}
		seen[stock.Code] = true
		pool = append(pool, stock)
	}
	for _, candidate := range report.Candidates {
		add(config.Stock{Code: candidate.Code, Name: candidate.Name, Sector: candidate.Sector})
	}
	for _, excluded := range report.Excluded {
		add(config.Stock{Code: excluded.Code})
	}
	if len(pool) != report.PoolSize {
		return nil, fmt.Errorf("first-stage report pool mismatch: reconstructed=%d reported=%d", len(pool), report.PoolSize)
	}
	return pool, nil
}

func previousTradingDay(cal *calendar.Calendar, day string) (string, error) {
	t, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	if err != nil {
		return "", err
	}
	for i := 0; i < 31; i++ {
		t = t.AddDate(0, 0, -1)
		candidate := calendar.Date(t)
		ok, err := cal.Trading(candidate)
		if err != nil {
			return "", err
		}
		if ok {
			return candidate, nil
		}
	}
	return "", errors.New("no previous trading day in calendar")
}
