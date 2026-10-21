package monitor

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

// prepareStartup refreshes the complete pool even when a report already exists.
// It only queues delivery: once this succeeds, notification retries must not
// restart the download. A failed refresh must not send a cached recommendation.
func (m *Monitor) prepareStartup(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := m.readState()
	if err != nil {
		return err
	}
	now := m.Now()
	asof, err := m.Calendar.Completed(now)
	if err != nil {
		return err
	}
	m.Log.Info("Startup synchronization started", "date", calendar.Date(now), "as_of", asof, "pool_size", len(m.Config.Stocks))
	report, err := m.Sync(ctx, asof)
	if err != nil {
		return err
	}
	latest, err := m.Calendar.Completed(m.Now())
	if err != nil {
		return err
	}
	if latest != asof {
		return errors.New("completed trading date changed during startup; synchronization will retry")
	}
	queued, err := m.enqueueReport(&state, report)
	if err != nil {
		return err
	}
	m.Log.Info("Startup synchronization completed", "as_of", asof, "target", report.Target, "queued_channels", queued)
	return nil
}

// Keep the first durable message per data date and channel, including pending
// or blocked attempts. Report contents (and therefore IDs) can change on refresh.
func (m *Monitor) enqueueReport(state *State, report selection.Report) (int, error) {
	if first := report.FirstStage; first != nil {
		if first.Status == firststage.StatusFailed {
			m.Log.Warn("First-stage screening failed", "as_of", report.AsOf, "reason", first.Reason)
		} else {
			m.Log.Info("First-stage screening completed", "as_of", report.AsOf, "status", first.Status, "eligible", first.Eligible, "candidates", len(first.Candidates))
		}
	}
	reason := ""
	switch {
	case len(m.channelNames()) == 0:
		reason = "no_enabled_channels"
	case calendar.Expired(report.Target, m.Now()):
		reason = "buying_session_already_open"
	}
	channels := []string{}
	if reason == "" {
		for _, name := range m.channelNames() {
			exists := false
			for _, event := range state.Events {
				if _, ok := event.Channels[name]; ok && event.AsOf == report.AsOf {
					exists = true
					break
				}
			}
			if !exists {
				channels = append(channels, name)
			}
		}
		if len(channels) == 0 {
			reason = "delivery_already_recorded"
		}
	}
	if reason != "" {
		m.Log.Info("Report enqueue skipped", "as_of", report.AsOf, "target", report.Target, "reason", reason)
		return 0, nil
	}
	cards, err := reportmessage.FromSelection(report)
	if err != nil {
		return 0, err
	}
	if err := enqueueCards(state, report.AsOf, cards, channels); err != nil {
		return 0, err
	}
	if err := m.writeState(*state); err != nil {
		return 0, err
	}
	return len(channels), nil
}
