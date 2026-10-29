package marketcontext

import (
	"context"
	"fmt"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type HangSengSyncResult struct {
	AsOf  string `json:"as_of"`
	Bars  int    `json:"bars"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

type KoreaMinuteSyncItem struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Bars  int    `json:"bars"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

type KoreaMinuteSyncResult struct {
	AsOf    string                `json:"as_of"`
	From    string                `json:"from"`
	Through string                `json:"through"`
	Results []KoreaMinuteSyncItem `json:"results"`
}

// SyncHangSeng fetches the shadow-only Hang Seng daily series. The caller is
// responsible for choosing a completed trading date.
func SyncHangSeng(ctx context.Context, c config.Config, db *storage.ContextDB, asOf string, log *logging.Logger) (HangSengSyncResult, error) {
	result := HangSengSyncResult{AsOf: asOf}
	client := market.NewTencentHangSengClient(config.Duration(c.Timeout), config.Duration(c.MarketContext.RequestInterval))
	client.Endpoint = c.MarketContext.HangSengEndpoint
	defer client.Close()
	bars, err := client.FetchDaily(ctx, asOf)
	if err != nil {
		return result, fmt.Errorf("fetch hang seng daily history: %w", err)
	}
	if err := db.SaveHangSengBars(ctx, bars); err != nil {
		return result, err
	}
	result.Bars = len(bars)
	result.First = bars[0].Date
	result.Last = bars[len(bars)-1].Date
	if log != nil {
		log.Info("Hang Seng shadow context synchronized", "as_of", asOf, "bars", len(bars), "last", result.Last)
	}
	return result, nil
}

// SyncKoreaMinutes fetches only the most recent Naver sessions. The cache is
// forward-only and is not a predictor input until enough independent days have
// accumulated.
func SyncKoreaMinutes(ctx context.Context, c config.Config, db *storage.ContextDB, asOf string, log *logging.Logger) (KoreaMinuteSyncResult, error) {
	result := KoreaMinuteSyncResult{AsOf: asOf, Results: []KoreaMinuteSyncItem{}}
	parsed, err := time.ParseInLocation("2006-01-02", asOf, market.Korea)
	if err != nil || parsed.Format("2006-01-02") != asOf {
		return result, fmt.Errorf("invalid korea minute as-of date")
	}
	from := parsed.AddDate(0, 0, -7)
	through := parsed.Add(16 * time.Hour)
	result.From = from.Format("2006-01-02 15:04")
	result.Through = through.Format("2006-01-02 15:04")
	client := market.NewNaverKoreaMinuteClient(config.Duration(c.Timeout), config.Duration(c.MarketContext.RequestInterval))
	client.Endpoint = c.MarketContext.KoreaMinuteEndpoint
	defer client.Close()
	for _, item := range []struct {
		code string
		name string
	}{
		{market.KoreaKOSPI, "韩国综合指数"},
		{market.KoreaKOSDAQ, "韩国创业板指数"},
	} {
		bars, err := client.FetchMinutes(ctx, item.code, from, through)
		if err != nil {
			return result, fmt.Errorf("fetch %s minutes: %w", item.code, err)
		}
		if err := db.SaveKoreaMinutes(ctx, item.code, bars); err != nil {
			return result, err
		}
		result.Results = append(result.Results, KoreaMinuteSyncItem{
			Code: item.code, Name: item.name, Bars: len(bars),
			First: bars[0].End.In(market.Korea).Format(time.RFC3339),
			Last:  bars[len(bars)-1].End.In(market.Korea).Format(time.RFC3339),
		})
	}
	if log != nil {
		log.Info("Korean minute context synchronized", "as_of", asOf, "from", result.From, "through", result.Through, "indices", len(result.Results))
	}
	return result, nil
}
