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
	signalDate, err := previousTradingDay(m.Calendar, evaluationDate)
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
	if previous.Target != evaluationDate {
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
	histories, err := m.Market.LoadHistories(ctx, m.Config.Stocks, evaluationDate, m.Config.Daily.Adjust)
	if err != nil {
		return false, err
	}
	review, err := firststage.EvaluateNextDay(*result, histories, m.Config.Stocks, firststage.ReviewOptions{
		SignalDate:     signalDate,
		EvaluationDate: evaluationDate,
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
	m.Log.Info("First-stage review queued", "signal", signalDate, "evaluation", evaluationDate, "hits", review.Hits, "evaluated", review.Evaluated, "pool_hits", review.PoolHits, "recall_pct", review.RecallPct)
	return true, nil
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
