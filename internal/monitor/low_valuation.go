package monitor

import (
	"context"
	"errors"
	"io/fs"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func (m *Monitor) enqueueLowValuation(ctx context.Context, state *State) (bool, error) {
	if !m.Config.LowValuation.Enabled || !m.Config.LowValuation.PushEnabled || m.LowValuationRecommendations == nil {
		return false, nil
	}
	recommendation, err := m.LowValuationRecommendations(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		m.Log.Warn("Low valuation recommendation unavailable", "reason", err.Error())
		return false, nil
	}
	if calendar.Expired(recommendation.Target, m.Now()) {
		return false, nil
	}
	message, err := reportmessage.LowValuationArticle(recommendation)
	if err != nil {
		m.Log.Warn("Low valuation article build failed", "as_of", recommendation.AsOf, "target", recommendation.Target, "reason", err.Error())
		return false, nil
	}
	channels := m.channelNamesForRoute(config.NotifyRouteLowValuation)
	pending := make([]string, 0, len(channels))
	for _, channel := range channels {
		if !recommendationArticleDelivered(*state, message.EventID, "low_valuation", channel) {
			pending = append(pending, channel)
		}
	}
	if len(pending) == 0 {
		return false, nil
	}
	if err := state.EnqueuePart(recommendation.AsOf, message, pending, "low_valuation", 101, ""); err != nil {
		return false, err
	}
	if err := m.writeState(*state); err != nil {
		return false, err
	}
	m.Log.Info("Low valuation recommendation queued", "as_of", recommendation.AsOf, "target", recommendation.Target,
		"id", recommendation.ID, "eligible", recommendation.Eligible, "selected", recommendation.Selected, "channels", pending)
	return true, nil
}

func recommendationArticleDelivered(state State, eventID, part, channel string) bool {
	for _, event := range state.Events {
		if event.Message.EventID != eventID || event.Part != part {
			continue
		}
		if _, ok := event.Channels[channel]; ok {
			return true
		}
	}
	return false
}
