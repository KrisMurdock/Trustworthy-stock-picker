package monitor

import (
	"context"
	"errors"
	"io/fs"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func (m *Monitor) enqueueValueTrapped(ctx context.Context, state *State) (bool, error) {
	if !m.Config.ValueTrapped.Enabled || !m.Config.ValueTrapped.PushEnabled || m.ValueTrappedRecommendations == nil {
		return false, nil
	}
	recommendation, err := m.ValueTrappedRecommendations(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		m.Log.Warn("Value-trapped recommendation unavailable", "reason", err.Error())
		return false, nil
	}
	if calendar.Expired(recommendation.Target, m.Now()) {
		return false, nil
	}
	message, err := reportmessage.ValueTrappedArticle(recommendation)
	if err != nil {
		m.Log.Warn("Value-trapped article build failed", "as_of", recommendation.AsOf, "target", recommendation.Target, "reason", err.Error())
		return false, nil
	}
	channels := m.channelNamesForRoute(config.NotifyRouteValueTrapped)
	pending := make([]string, 0, len(channels))
	for _, channel := range channels {
		if !recommendationArticleDelivered(*state, message.EventID, "value_trapped", channel) {
			pending = append(pending, channel)
		}
	}
	if len(pending) == 0 {
		return false, nil
	}
	if err := state.EnqueuePart(recommendation.AsOf, message, pending, "value_trapped", 102, ""); err != nil {
		return false, err
	}
	if err := m.writeState(*state); err != nil {
		return false, err
	}
	m.Log.Info("Value-trapped recommendation queued", "as_of", recommendation.AsOf, "target", recommendation.Target,
		"id", recommendation.ID, "eligible", recommendation.Eligible, "selected", recommendation.Selected, "channels", pending)
	return true, nil
}
