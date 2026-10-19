package selection

import (
	"context"
	"math"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func executionFixture() (config.Config, []market.History, []market.History) {
	c := config.Defaults()
	c.Strategy.Enabled = true
	c.Selection.Enabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}}
	bars := []market.Bar{}
	date, _ := time.Parse("2006-01-02", "2026-01-01")
	for len(bars) < 190 {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			i := float64(len(bars))
			p := 10 + i*0.02 + 0.1*math.Sin(i)
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: p, Close: p + 0.02, High: p + 0.15, Low: p - 0.1, Volume: 1000})
		}
		date = date.AddDate(0, 0, 1)
	}
	asof := bars[len(bars)-1].Date
	adjusted := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: asof, FetchedAt: time.Now(), Bars: bars}
	raw := adjusted
	raw.Adjust = "none"
	raw.DataField = "day"
	raw.Bars = append([]market.Bar(nil), bars...)
	return c, []market.History{adjusted}, []market.History{raw}
}

func singleDayMinuteHistory(date string, bar market.Bar) market.MinuteHistory {
	day, _ := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	bars := []market.MinuteBar{}
	for i := 0; i < 48; i++ {
		minute := 575 + i*5
		if i >= 24 {
			minute = 785 + (i-24)*5
		}
		bars = append(bars, market.MinuteBar{
			End: day.Add(time.Duration(minute) * time.Minute), Open: bar.Close, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: 100, Final: true,
		})
	}
	return market.MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: day.Add(18 * time.Hour), Bars: bars}
}

func TestBuildWithExecutionMinutesMarksUnknownChanAsFiltered(t *testing.T) {
	c, histories, raw := executionFixture()
	asof := histories[0].AsOf
	minutes := singleDayMinuteHistory(asof, raw[0].Bars[len(raw[0].Bars)-1])
	report, err := BuildWithExecutionMinutes(context.Background(), c, histories, raw, []market.MinuteHistory{minutes}, asof, "2026-10-20", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Ranking) != 1 || report.Ranking[0].Chan == nil {
		t.Fatalf("Chan evidence missing: %+v", report.Ranking)
	}
	if report.Ranking[0].Chan.Status != "unknown" || report.Ranking[0].Recommended {
		t.Fatalf("unknown Chan evidence should disable recommendation: %+v", report.Ranking[0])
	}
	if len(report.Filtered) != 1 || report.Filtered[0].Reason != "chan_unknown" {
		t.Fatalf("wrong filtered reason: %+v", report.Filtered)
	}
	if report.Passed != 0 || len(report.Selected) != 0 {
		t.Fatalf("unknown Chan evidence entered selection: passed=%d selected=%d", report.Passed, len(report.Selected))
	}
}

func TestRecommendationFilterReasonPrefersChanEvidence(t *testing.T) {
	tests := []struct {
		name  string
		risks []string
		want  string
	}{
		{name: "chan fail", risks: []string{"chan_filtered", "chan_unknown"}, want: "chan_filtered"},
		{name: "chan unknown", risks: []string{"chan_unknown"}, want: "chan_unknown"},
		{name: "other gate", risks: []string{"insufficient_or_unprofitable_evidence"}, want: "strategy_evidence_or_risk_gate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := recommendationFilterReason(test.risks); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEnforceStructureGuardDoesNotBypassInsufficientDailyHistory(t *testing.T) {
	c, histories, raw := executionFixture()
	c.StructureGuard.Mode = "enforce"
	asof := histories[0].AsOf
	minutes := singleDayMinuteHistory(asof, raw[0].Bars[len(raw[0].Bars)-1])
	report, err := BuildWithExecutionMinutes(context.Background(), c, histories, raw, []market.MinuteHistory{minutes}, asof, "2026-10-20", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != 3 || report.GuardPassed != 0 || report.Passed != 0 || len(report.Selected) != 0 {
		t.Fatalf("insufficient daily history bypassed structure guard: %+v", report)
	}
	if len(report.Ranking) != 1 || report.Ranking[0].StructureGuard == nil || report.Ranking[0].StructureGuard.GuardStatus != "unknown" {
		t.Fatalf("structure evidence missing: %+v", report.Ranking)
	}
}
