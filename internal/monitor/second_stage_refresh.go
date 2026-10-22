package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func secondStageDeadline(asOf, publishBy string) (time.Time, error) {
	deadline, err := time.ParseInLocation("2006-01-02 15:04", asOf+" "+publishBy, market.Shanghai)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid second-stage publish_by: %w", err)
	}
	return deadline, nil
}

// prepareSecondStage waits for the external model artifacts until the
// configured publication deadline. Market data and the first-stage report are
// already durable at this point; only reranking and its notification are held.
func (m *Monitor) prepareSecondStage(ctx context.Context, state *State, report selection.Report) (selection.Report, bool, error) {
	second := report.SecondStage
	if !m.Config.SecondStage.Enabled || second == nil || !second.Degraded {
		m.secondStagePending = false
		return report, false, nil
	}
	if state != nil && m.secondStageDeliveryRecorded(state, report.AsOf) {
		m.secondStagePending = false
		return report, false, nil
	}
	deadline, err := secondStageDeadline(report.AsOf, m.Config.SecondStage.PublishBy)
	if err != nil {
		return report, false, err
	}
	refreshed, err := selection.RecomputeSecondStage(report, m.Config, m.Now())
	if err == nil && refreshed.SecondStage != nil && !refreshed.SecondStage.Degraded {
		if m.Market != nil && m.Market.HasDatabase() {
			err = m.Market.ReplaceReport(ctx, refreshed)
		} else {
			err = m.Store.Write("reports/"+report.AsOf+".json", refreshed)
		}
		if err != nil {
			return report, false, err
		}
		m.secondStagePending = false
		m.Log.Info("Second-stage artifacts loaded", "as_of", report.AsOf, "models", len(refreshed.SecondStage.Models), "blend_id", refreshed.SecondStage.Blend.ID)
		return refreshed, false, nil
	}
	if !m.Now().Before(deadline) {
		m.secondStagePending = false
		if err != nil {
			m.Log.Warn("Second-stage artifacts unavailable at deadline; publishing degraded result", "as_of", report.AsOf, "publish_by", deadline.Format("15:04"), "reason", err.Error())
		} else {
			m.Log.Warn("Second-stage artifacts incomplete at deadline; publishing degraded result", "as_of", report.AsOf, "publish_by", deadline.Format("15:04"))
		}
		return report, false, nil
	}
	m.secondStagePending = true
	if err != nil {
		m.Log.Warn("Second-stage refresh deferred", "as_of", report.AsOf, "publish_by", deadline.Format("15:04"), "reason", err.Error())
	} else {
		m.Log.Info("Second-stage artifacts incomplete; delivery deferred", "as_of", report.AsOf, "publish_by", deadline.Format("15:04"))
	}
	return report, true, nil
}

func (m *Monitor) secondStageDeliveryRecorded(state *State, asOf string) bool {
	channels := m.channelNamesForRoute(config.NotifyRouteSecondStage)
	if len(channels) == 0 {
		return false
	}
	return len(pendingChannelNames(state, asOf, channels)) == 0
}
