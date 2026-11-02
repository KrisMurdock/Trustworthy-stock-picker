package hkconnect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkrecommend"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/indicators"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	recommendationMarket      = hkrecommend.Market
	recommendationHistoryDays = 400
)

const RecommendationSchemaVersion = hkrecommend.SchemaVersion

type Recommendation = hkrecommend.Recommendation
type RecommendationCandidate = hkrecommend.RecommendationCandidate
type RecommendationReason = hkrecommend.RecommendationReason

func BuildRecommendation(c config.Config, stocks []config.Stock, histories []market.History, asOf, target string, now time.Time) (Recommendation, error) {
	rec := Recommendation{
		Version: RecommendationSchemaVersion, Market: recommendationMarket, AsOf: asOf, Target: target,
		GeneratedAt: now.In(market.Shanghai), UniverseTotal: len(stocks), TopN: c.HKConnect.TopN,
		MinScore: c.Selection.MinScore, Candidates: []RecommendationCandidate{},
		Rules: []string{
			"收盘价 > MA20 且 MA5 > MA10 > MA20",
			fmt.Sprintf("量比 ≥ %.2f 且 MACD DIF > DEA", c.Selection.MinVolumeRatio),
			fmt.Sprintf("RSI14 %.0f～%.0f，日涨跌幅 %.1f%%～%.1f%%", c.Selection.RSIMin, c.Selection.RSIMax, c.Selection.MinDayChange, c.Selection.MaxDayChange),
			fmt.Sprintf("综合趋势分 ≥ %d，按分数降序选取前 %d 只", c.Selection.MinScore, c.HKConnect.TopN),
		},
		Limitations: []string{
			"本稿是港股通日线规则观察，独立于 A 股初筛、精排模型和成交模拟。",
			"港股实行 T+0 且没有 A 股式涨跌停，本文不模拟买卖成交，也不构成交易建议。",
			"机会分来自价量指标，不是成功概率或收益承诺；停牌和数据不完整标的不进入排名。",
		},
	}
	if asOf == "" || target == "" || target <= asOf {
		return rec, errors.New("invalid HK recommendation dates")
	}
	if c.HKConnect.TopN < 1 || c.HKConnect.TopN > 20 {
		return rec, errors.New("invalid HK recommendation top_n")
	}
	byCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		if history.Adjust != "qfq" || history.AsOf != asOf || history.Validate() != nil {
			return rec, fmt.Errorf("invalid HK recommendation history for %s", history.Code)
		}
		if _, exists := byCode[history.Code]; exists {
			return rec, errors.New("duplicate HK recommendation history")
		}
		byCode[history.Code] = history
	}
	reasons := map[string]int{}
	ranked := make([]RecommendationCandidate, 0, len(stocks))
	for _, stock := range stocks {
		history, ok := byCode[stock.Code]
		if !ok {
			reasons["missing_data"]++
			continue
		}
		if len(history.Bars) < 2 {
			reasons["insufficient_history"]++
			continue
		}
		last := history.Bars[len(history.Bars)-1]
		if last.Date != asOf {
			reasons["stale_or_suspended"]++
			continue
		}
		if last.Volume == 0 {
			reasons["no_trading_volume"]++
			continue
		}
		metrics, err := indicators.Score(history.Bars)
		if err != nil {
			reasons["insufficient_or_invalid_history"]++
			continue
		}
		rec.Eligible++
		change := (last.Close/history.Bars[len(history.Bars)-2].Close - 1) * 100
		name := strings.ToUpper(strings.TrimSpace(history.Name))
		reason := ""
		switch {
		case strings.HasPrefix(name, "ST") || strings.HasPrefix(name, "*ST") || strings.HasPrefix(name, "S*ST") || strings.HasPrefix(name, "SST") || strings.Contains(name, "退"):
			reason = "special_treatment"
		case !(last.Close > metrics.MA20 && metrics.MA5 > metrics.MA10 && metrics.MA10 > metrics.MA20):
			reason = "no_bullish_alignment"
		case metrics.VolumeRatio < c.Selection.MinVolumeRatio || metrics.DIF <= metrics.DEA:
			reason = "insufficient_volume_or_macd"
		case metrics.RSI14 < c.Selection.RSIMin || metrics.RSI14 > c.Selection.RSIMax || change < c.Selection.MinDayChange || change > c.Selection.MaxDayChange:
			reason = "overheat_or_weakness"
		case metrics.Score < c.Selection.MinScore:
			reason = "score_below_threshold"
		}
		if reason != "" {
			reasons[reason]++
			continue
		}
		ranked = append(ranked, RecommendationCandidate{Code: stock.Code, Name: history.Name, Close: last.Close,
			ChangePct: change, Metrics: metrics, Chart: strategy.BuildArticleChart(history, nil, nil)})
	}
	if rec.Eligible == 0 {
		return rec, errors.New("no HK Connect stocks have complete eligible daily data")
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Metrics.Score != ranked[j].Metrics.Score {
			return ranked[i].Metrics.Score > ranked[j].Metrics.Score
		}
		return ranked[i].Code < ranked[j].Code
	})
	rec.Passed = len(ranked)
	limit := min(len(ranked), c.HKConnect.TopN)
	rec.Candidates = append(rec.Candidates, ranked[:limit]...)
	for index := range rec.Candidates {
		rec.Candidates[index].Rank = index + 1
	}
	rec.Selected = len(rec.Candidates)
	for reason, count := range reasons {
		rec.Filtered = append(rec.Filtered, RecommendationReason{Reason: reason, Count: count})
	}
	sort.Slice(rec.Filtered, func(i, j int) bool { return rec.Filtered[i].Reason < rec.Filtered[j].Reason })
	identity := rec
	identity.ID = ""
	identity.GeneratedAt = time.Time{}
	data, err := json.Marshal(identity)
	if err != nil {
		return rec, errors.New("cannot encode HK recommendation identity")
	}
	sum := sha256.Sum256(data)
	rec.ID = hex.EncodeToString(sum[:12])
	return rec, nil
}

func GenerateRecommendation(ctx context.Context, c config.Config, db *storage.AllMarketDB, tradingDays TradingDaysSource, asOf string, now time.Time) (Recommendation, error) {
	if db == nil || tradingDays == nil {
		return Recommendation{}, errors.New("HK recommendation dependencies are unavailable")
	}
	target := ""
	// The HSI calendar only contains sessions that have already produced bars,
	// so a request entirely in the future is normally empty. Prefer a real
	// future session when the source can provide one, otherwise fall back to
	// the next Hong Kong weekday.
	if days, err := tradingDays.TradingDays(ctx, addDays(asOf, 1), addDays(asOf, 21)); err == nil && len(days) > 0 {
		target = days[0]
	}
	if target == "" {
		var err error
		target, err = nextWeekdayTarget(asOf)
		if err != nil {
			return Recommendation{}, err
		}
	}
	stocks, histories, err := db.LoadAdjustedHistories(ctx, asOf, addDays(asOf, -recommendationHistoryDays))
	if err != nil {
		return Recommendation{}, err
	}
	rec, err := BuildRecommendation(c, stocks, histories, asOf, target, now)
	if err != nil {
		return Recommendation{}, err
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return Recommendation{}, errors.New("cannot encode HK recommendation")
	}
	if err := db.SaveRecommendation(ctx, storage.RecommendationArtifact{Market: recommendationMarket, AsOf: rec.AsOf,
		ID: rec.ID, Target: rec.Target, BuiltAt: rec.GeneratedAt, Payload: payload}); err != nil {
		return Recommendation{}, err
	}
	return rec, nil
}

func LoadLatestRecommendation(ctx context.Context, root string, requireCIFS bool) (Recommendation, error) {
	if _, err := os.Stat(filepath.Join(root, "hk-market.db")); errors.Is(err, fs.ErrNotExist) {
		return Recommendation{}, fs.ErrNotExist
	} else if err != nil {
		return Recommendation{}, err
	}
	db, err := storage.OpenHKMarketView(root, requireCIFS)
	if err != nil {
		return Recommendation{}, err
	}
	defer db.Close()
	artifact, err := db.LatestRecommendation(ctx, recommendationMarket)
	if errors.Is(err, fs.ErrNotExist) || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return Recommendation{}, fs.ErrNotExist
	}
	if err != nil {
		return Recommendation{}, err
	}
	var rec Recommendation
	if err := json.Unmarshal(artifact.Payload, &rec); err != nil {
		return rec, errors.New("invalid stored HK recommendation")
	}
	if rec.Version != RecommendationSchemaVersion || rec.ID != artifact.ID || rec.AsOf != artifact.AsOf || rec.Target != artifact.Target || rec.Market != recommendationMarket {
		return rec, errors.New("stored HK recommendation identity mismatch")
	}
	return rec, nil
}

func nextWeekdayTarget(asOf string) (string, error) {
	day, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return "", errors.New("invalid HK recommendation as-of date")
	}
	for {
		day = day.AddDate(0, 0, 1)
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			return day.Format("2006-01-02"), nil
		}
	}
}
