package monitor

import (
	"context"
	"errors"
	"io/fs"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func (m *Monitor) enqueueHKConnect(ctx context.Context, state *State) (bool, error) {
	if !m.Config.HKConnect.Enabled || !m.Config.HKConnect.PushEnabled || m.HKRecommendations == nil {
		return false, nil
	}
	recommendation, err := m.HKRecommendations(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		m.Log.Warn("HK Connect recommendation unavailable", "reason", err.Error())
		return false, nil
	}
	if calendar.Expired(recommendation.Target, m.Now()) {
		return false, nil
	}
	message, err := reportmessage.HKConnectArticle(recommendation)
	if err != nil {
		m.Log.Warn("HK Connect article build failed", "as_of", recommendation.AsOf, "target", recommendation.Target, "reason", err.Error())
		return false, nil
	}
	channels := m.channelNamesForRoute(config.NotifyRouteHKConnect)
	pending := make([]string, 0, len(channels))
	for _, channel := range channels {
		if !hkRecommendationDelivered(*state, recommendation.AsOf, channel) {
			pending = append(pending, channel)
		}
	}
	if len(pending) == 0 {
		return false, nil
	}
	if err := state.EnqueuePart(recommendation.AsOf, message, pending, "hk_connect", 100, ""); err != nil {
		return false, err
	}
	if err := m.writeState(*state); err != nil {
		return false, err
	}
	m.Log.Info("HK Connect recommendation queued", "as_of", recommendation.AsOf, "target", recommendation.Target, "id", recommendation.ID, "selected", recommendation.Selected, "channels", pending)
	return true, nil
}

func hkRecommendationDelivered(state State, asOf, channel string) bool {
	for _, event := range state.Events {
		if event.AsOf != asOf || event.Part != "hk_connect" {
			continue
		}
		if _, ok := event.Channels[channel]; ok {
			return true
		}
	}
	return false
}
