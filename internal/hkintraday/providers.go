package hkintraday

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkconnect"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func NewProduction(c config.Config, log *logging.Logger) (*Monitor, func()) {
	timeout := config.Duration(c.Timeout)
	dailyInterval := config.Duration(c.Market.RequestInterval)
	minute := market.NewHKMinuteClient(timeout, config.Duration(c.HKIntraday.RequestInterval), c.Market.EastmoneyEndpoint)
	calendar := market.NewHKDailyClient(timeout, dailyInterval, c.Market.HKEndpoint, c.Market.HKEndpointFallback)
	sina := market.NewSinaHKConnectUniverseClient(timeout, dailyInterval, c.Market.HKUniverseEndpoint, c.Market.HKQuoteEndpoint)
	fallback := market.NewHKConnectUniverseClient(timeout, dailyInterval, c.Market.HKUniverseFallback)
	universe := hkconnect.FailoverUniverse{Sources: []hkconnect.UniverseSource{sina, fallback}}
	closeSources := func() {
		minute.Close()
		calendar.Close()
		sina.Close()
		fallback.Close()
	}
	return New(c, minute, universe, calendar, log), closeSources
}
