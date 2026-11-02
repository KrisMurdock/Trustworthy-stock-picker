package selection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestAttachTrappedSignalsUsesRawHistoryAndTurnover(t *testing.T) {
	report := Report{
		Adjust:  "qfq",
		Ranking: []strategy.Candidate{{Code: "sz000001"}, {Code: "sz000002"}, {Code: "sz000003"}},
		Selected: []Candidate{
			{Code: "sz000001", Ranking: &strategy.Candidate{Code: "sz000001"}},
		},
	}
	raw := trappedTestHistory("sz000001", 120, 80, 10.3, 10)
	adjusted := trappedTestHistory("sz000001", 120, 80, 20.3, 20)
	raw.Adjust = "none"
	adjusted.Adjust = "qfq"
	adjusted.DataField = "qfqday"
	otherAdjusted := trappedTestHistory("sz000002", 120, 80, 20.3, 20)
	otherAdjusted.Adjust = "qfq"
	otherAdjusted.DataField = "qfqday"
	histories := []market.History{adjusted, otherAdjusted}
	metrics := map[string]market.DailyMetricHistory{
		"sz000001": trappedTestMetrics(raw, 0.001),
	}

	attachTrappedSignals(&report, histories, []market.History{raw}, metrics)

	if report.Ranking[0].Trapped == nil || report.Ranking[0].Trapped.Status != strategy.TrappedStatusTriggered {
		t.Fatalf("triggered signal not attached: %+v", report.Ranking[0].Trapped)
	}
	if report.Ranking[0].Trapped.CurrentPrice != 10 {
		t.Fatalf("adjusted price was used: %+v", report.Ranking[0].Trapped)
	}
	if report.Selected[0].Ranking == nil || report.Selected[0].Ranking.Trapped == nil {
		t.Fatal("selected candidate was not aligned with ranking")
	}
	if report.Ranking[1].Trapped == nil || report.Ranking[1].Trapped.Reasons[0] != "unadjusted_history_unavailable" {
		t.Fatalf("adjusted history must not be used as cost input: %+v", report.Ranking[1].Trapped)
	}
	if report.Ranking[2].Trapped == nil || report.Ranking[2].Trapped.Reasons[0] != "unadjusted_history_unavailable" {
		t.Fatalf("missing raw history should be explicit: %+v", report.Ranking[2].Trapped)
	}
	encoded, err := json.Marshal(report.Ranking[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"trapped_signal"`) || !strings.Contains(string(encoded), `"turnover_adjusted_v3"`) {
		t.Fatalf("v3 trapped signal is not persisted in candidate JSON: %s", encoded)
	}
}

func trappedTestHistory(code string, total, trapped int, trappedPrice, currentPrice float64) market.History {
	bars := make([]market.Bar, total)
	for i := range bars {
		price := currentPrice
		if i < trapped {
			price = trappedPrice
		}
		bars[i] = market.Bar{
			Date:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open:   price,
			High:   price,
			Low:    price,
			Close:  price,
			Volume: 100,
		}
	}
	return market.History{
		Version: 1, Code: code, Name: "测试", Source: "sina", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
		FetchedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Bars: bars,
	}
}

func trappedTestMetrics(history market.History, rate float64) market.DailyMetricHistory {
	metrics := market.DailyMetricHistory{
		Version: 1, Code: history.Code, Source: "sohu", FetchedAt: history.FetchedAt,
	}
	for _, bar := range history.Bars {
		if bar.Volume <= 0 {
			continue
		}
		metrics.Metrics = append(metrics.Metrics, market.DailyMetric{Date: bar.Date, TurnoverRatePct: rate, AmountCNY: 1e8})
	}
	return metrics
}
