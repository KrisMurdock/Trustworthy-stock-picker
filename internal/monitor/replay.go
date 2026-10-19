package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"time"
)

// Replay is read-only and never enqueues a notification. It requires the same
// build: a matching strategy name alone cannot promise identical calculations.
func (m *Monitor) Replay(ctx context.Context, revision string) (selection.Report, error) {
	if m.Market == nil {
		return selection.Report{}, errors.New("replay requires a market database")
	}
	recorded, err := m.Market.LoadReportRevision(ctx, revision)
	if err != nil {
		return selection.Report{}, err
	}
	if recorded.InputID == "" || recorded.Provenance == nil || recorded.Provenance.Build == "" {
		return selection.Report{}, errors.New("legacy report has no reproducible input or build")
	}
	if recorded.Provenance.Build != m.BuildVersion {
		return selection.Report{}, errors.New("replay requires the recorded build; use the matching binary or image")
	}
	var params config.Config
	if err := json.Unmarshal(recorded.Provenance.Parameters, &params); err != nil {
		return selection.Report{}, err
	}
	id, _ := selection.ConfigDigest(params)
	if id != recorded.Provenance.ConfigID {
		return selection.Report{}, errors.New("report parameter digest mismatch")
	}
	items, err := m.Market.LoadReportInputs(ctx, recorded.InputID)
	if err != nil {
		return selection.Report{}, err
	}
	var adjusted, raw []market.History
	for _, item := range items {
		if item.Adjusted != nil {
			adjusted = append(adjusted, *item.Adjusted)
			if item.Raw != nil {
				raw = append(raw, *item.Raw)
			} else {
				raw = append(raw, *item.Adjusted)
			}
		}
	}
	replay := *m
	replay.Config = params
	replay.Now = func() time.Time { return recorded.GeneratedAt }
	result, err := replay.buildReport(ctx, adjusted, raw, recorded.AsOf, recorded.Target)
	if err != nil {
		return result, err
	}
	result.InputID = recorded.InputID
	result.Revision = recorded.Revision
	return result, nil
}
